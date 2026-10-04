#!/usr/bin/env python3
"""Run via ssh mmwx-prod python3 - < this-file; SELECT only, aggregate output only.

Database credentials and authentication values never leave the remote process.
No remote files, schema, data, settings, services or APIs are changed.
"""
import collections
import datetime
import json
import os
import re
import subprocess


config = json.load(open('/etc/mmwx/data/database.json'))
env = dict(os.environ, PGHOST=str(config['host']), PGPORT=str(config['port']),
           PGUSER=config['username'], PGPASSWORD=config['password'],
           PGDATABASE=config['database'], PGSSLMODE=config.get('ssl_mode', 'disable'),
           PGOPTIONS='-c statement_timeout=15000 -c default_transaction_read_only=on')


def query(sql):
    result = subprocess.run(['psql', '-X', '-q', '-A', '-t', '-v', 'ON_ERROR_STOP=1'],
                            input='BEGIN READ ONLY;\n' + sql + ';\nROLLBACK;\n',
                            text=True, capture_output=True, env=env)
    if result.returncode:
        raise RuntimeError('Read-only query failed; stderr deliberately suppressed')
    return json.loads(result.stdout.strip())


def rows(sql):
    return query("SELECT COALESCE(json_agg(t), '[]'::json) FROM (" + sql + ') t')


def obj(raw):
    if isinstance(raw, (dict, list)):
        return raw
    try:
        return json.loads(raw or '{}')
    except (ValueError, TypeError):
        return {}


tables = {r['table_name'] for r in rows("SELECT table_name FROM information_schema.tables WHERE table_schema='public'")}


def optional(table, sql):
    return rows(sql) if table in tables else []


users = rows('SELECT username,email,role,is_active,package_id FROM users')
user_by_name = {u['username']: u for u in users}
backups = optional('mmwxc_user_disabled_credentials', 'SELECT username,server_id,inbound_tag,protocol,original_credential,disabled_credential FROM mmwxc_user_disabled_credentials')
states = optional('mmwxc_user_lifecycle', 'SELECT username,desired_state,effective_state,pending_count FROM mmwxc_user_lifecycle')
state_by_user = {s['username']: s for s in states}
operations = optional('mmwxc_user_lifecycle_operations', 'SELECT username,operation,state FROM mmwxc_user_lifecycle_operations')
assignments = rows('SELECT id,username,package_id,status FROM user_package_assignments')
packages = rows('SELECT id,nodes FROM packages')
refs = []
for table in ['user_inbound_configs', 'package_assignment_inbound_configs']:
    refs += optional(table, "SELECT username,server_id,inbound_tag,protocol,credential_json,'" + table + "' AS source FROM " + table)
for table in ['user_subaccounts', 'package_assignment_subaccounts']:
    refs += optional(table, "SELECT a.username,s.id AS server_id,n.inbound_tag,n.protocol,a.credential_json,'" + table + "' AS source FROM " + table + " a JOIN nodes n ON n.id=a.routed_node_id JOIN remote_servers s ON s.name=n.original_server")
snapshots = optional('server_xray_config_snapshots', 'SELECT DISTINCT ON (server_id) server_id,config_json,source,status,created_at FROM server_xray_config_snapshots ORDER BY server_id,created_at DESC,id DESC')
earliest_snapshots = optional('server_xray_config_snapshots', "SELECT DISTINCT ON (s.server_id,inbound->>'tag') s.server_id,jsonb_build_object('inbounds',jsonb_build_array(inbound)) AS config_json,s.source FROM server_xray_config_snapshots s CROSS JOIN LATERAL jsonb_array_elements(COALESCE(s.config_json::jsonb->'inbounds','[]'::jsonb)) inbound ORDER BY s.server_id,inbound->>'tag',s.created_at,s.id")
keys = {'vless': 'id', 'vmess': 'id', 'trojan': 'password', 'shadowsocks': 'password', 'ss': 'password', 'hysteria': 'auth', 'hysteria2': 'auth', 'hy2': 'auth', 'socks': 'user', 'http': 'user', 'snell': 'psk', 'mieru': 'username', 'anytls': 'password'}


def auth(protocol, credential):
    credential = obj(credential)
    if not isinstance(credential, dict):
        return ''
    return str(credential.get(keys.get(protocol.lower(), ''), '')).strip()


def entries(inbound):
    settings = inbound.get('settings', {})
    for key in ['clients', 'accounts', 'users']:
        if isinstance(settings.get(key), list):
            return [e for e in settings[key] if isinstance(e, dict)]
    return [settings] if any(k in settings for k in ['password', 'auth', 'psk']) else []


current = {}
for snapshot in snapshots:
    for inbound in obj(snapshot['config_json']).get('inbounds', []):
        current[(snapshot['server_id'], inbound.get('tag', ''))] = inbound
admin_names = set()
for user in users:
    if user['role'] == 'admin':
        admin_names.add(user['username'].lower())
        if user['email']:
            admin_names.add(user['email'].lower())


def is_admin(credential):
    for key in ['email', 'username', 'user', 'name']:
        identity = str(credential.get(key, '')).strip().lower()
        for admin in admin_names:
            if identity == admin or ('@' not in admin and (identity.startswith(admin + '__') or identity == 'mmw@' + admin + '.me')):
                return True
    return False


defaults = {}
for snapshot in earliest_snapshots:
    for inbound in obj(snapshot['config_json']).get('inbounds', []):
        key = (snapshot['server_id'], inbound.get('tag', ''))
        if key not in defaults:
            defaults[key] = [e for e in entries(inbound) if is_admin(e)] if snapshot['source'] == 'master_write' else []

backup_summary = collections.Counter({key: 0 for key in [
    'total', 'deleted_user_rows', 'no_current_credential_ref', 'official_ref_auth_drift',
    'retained_while_enabled', 'no_latest_snapshot_inbound', 'latest_snapshot_matches_disabled',
    'latest_snapshot_matches_original', 'latest_snapshot_auth_drift']})
for backup in backups:
    protocol = backup['protocol']
    key = (backup['server_id'], backup['inbound_tag'])
    original = auth(protocol, backup['original_credential'])
    disabled = auth(protocol, backup['disabled_credential'])
    backup_summary['total'] += 1
    if backup['username'] not in user_by_name:
        backup_summary['deleted_user_rows'] += 1
    relevant = [r for r in refs if r['username'] == backup['username'] and (r['server_id'], r['inbound_tag']) == key]
    if not relevant:
        backup_summary['no_current_credential_ref'] += 1
    elif not any(auth(protocol, r['credential_json']) in [original, disabled] for r in relevant):
        backup_summary['official_ref_auth_drift'] += 1
    if state_by_user.get(backup['username'], {}).get('effective_state', 'enabled') == 'enabled':
        backup_summary['retained_while_enabled'] += 1
    if key not in current:
        backup_summary['no_latest_snapshot_inbound'] += 1
    else:
        live_values = [auth(protocol, e) for e in entries(current[key])]
        if disabled in live_values:
            backup_summary['latest_snapshot_matches_disabled'] += 1
        elif original in live_values:
            backup_summary['latest_snapshot_matches_original'] += 1
        else:
            backup_summary['latest_snapshot_auth_drift'] += 1

by_auth = collections.defaultdict(set)
ref_groups = collections.defaultdict(list)
for ref in refs:
    value = auth(ref['protocol'], ref['credential_json'])
    if not value:
        continue
    key = (ref['server_id'], ref['inbound_tag'], ref['protocol'], value)
    by_auth[key].add(ref['username'])
    ref_groups[key].append(ref)
shared = {key: owners for key, owners in by_auth.items() if len(owners) > 1}
admin_shared = set()
for key, owners in by_auth.items():
    for credential in defaults.get(key[:2], []):
        if auth(key[2], credential) == key[3] and any(user_by_name.get(u, {}).get('role') != 'admin' for u in owners):
            admin_shared.add(key)

# Node ownership can share a credential even when no user_inbound_configs row exists.
# Only count matches proven against the latest stored inbound and its SS2022 server key.
owned_nodes = rows("SELECT n.id,n.username,n.protocol,n.parsed_config,n.clash_config,n.inbound_tag,s.id AS server_id FROM nodes n JOIN remote_servers s ON s.name=n.original_server WHERE COALESCE(n.inbound_tag,'')<>''")
all_auth_owners = {key: set(owners) for key, owners in by_auth.items()}
node_counts = collections.Counter(total=len(owned_nodes), matched=0, unmatched=0)


def secrets(value):
    found = set()
    if isinstance(value, dict):
        for key, item in value.items():
            if key.lower() in ['password', 'passwd', 'pass', 'id', 'uuid', 'psk', 'auth', 'token'] and isinstance(item, str) and item:
                found.add(item)
            elif isinstance(item, (dict, list)):
                found.update(secrets(item))
    elif isinstance(value, list):
        for item in value:
            found.update(secrets(item))
    return found


for node in owned_nodes:
    inbound_key = (node['server_id'], node['inbound_tag'])
    inbound = current.get(inbound_key, {})
    protocol = inbound.get('protocol', node['protocol'])
    candidates = secrets(obj(node['parsed_config'])) | secrets(obj(node['clash_config']))
    settings = inbound.get('settings', {})
    if str(settings.get('method', '')).startswith('2022-blake3-'):
        prefix = str(settings.get('password', '')) + ':'
        candidates |= {value[len(prefix):] for value in list(candidates) if value.startswith(prefix)}
    matches = {auth(protocol, entry) for entry in entries(inbound)} & candidates
    matches.discard('')
    node_counts['matched' if matches else 'unmatched'] += 1
    for value in matches:
        all_auth_owners.setdefault((*inbound_key, protocol, value), set()).add(node['username'])
all_shared = {key: owners for key, owners in all_auth_owners.items() if len(owners) > 1}
all_admin_shared = set()
for key, owners in all_auth_owners.items():
    if any(auth(key[2], credential) == key[3] for credential in defaults.get(key[:2], [])) and any(user_by_name.get(u, {}).get('role') != 'admin' for u in owners):
        all_admin_shared.add(key)

bound = collections.defaultdict(set)
all_bound = collections.defaultdict(set)
for user in users:
    if user['package_id'] is not None:
        bound[user['package_id']].add(user['username'])
        all_bound[user['package_id']].add(user['username'])
for assignment in assignments:
    all_bound[assignment['package_id']].add(assignment['username'])
    if assignment['status'] in [None, 'active']:
        bound[assignment['package_id']].add(assignment['username'])
single = {p['id'] for p in packages if len(bound[p['id']]) == 1}
deletable_single = {p for p in single if all(user_by_name.get(u, {}).get('role') != 'admin' for u in bound[p])}
ignored = {p for p in single if all_bound[p] - bound[p]}

constraints = rows("SELECT c.conrelid::regclass::text AS table_name,c.conname,pg_get_constraintdef(c.oid) AS definition FROM pg_constraint c WHERE c.contype='f' AND c.conrelid::regclass::text IN ('mmwxc_user_disabled_credentials','mmwxc_user_lifecycle','mmwxc_user_lifecycle_operations') ORDER BY 1,2")
group_counts = optional('mmwxc_package_traffic_groups', 'SELECT package_id,COUNT(*) AS count FROM mmwxc_package_traffic_groups GROUP BY package_id')
node_owners = rows('SELECT id,username FROM nodes')
explicit_shared_nodes = set()
implicit_shared_nodes = set()
for package in packages:
    member_ids = obj(package['nodes'])
    if not isinstance(member_ids, list):
        continue
    for node in node_owners:
        if user_by_name.get(node['username'], {}).get('role') == 'admin':
            continue
        if not (bound[package['id']] - {node['username']}):
            continue
        if node['id'] in member_ids:
            explicit_shared_nodes.add(node['id'])
        elif not member_ids:
            implicit_shared_nodes.add(node['id'])
forward_orphans = optional('forward_chain_nodes', "SELECT count(*) AS rows_with_missing_owner FROM forward_chain_nodes f WHERE COALESCE(f.owner_username,'')<>'' AND NOT EXISTS(SELECT 1 FROM users u WHERE u.username=f.owner_username)")

result = {
    'captured_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'read_only': True,
    'consistency': 'Each query uses BEGIN READ ONLY; queries are not one shared snapshot.',
    'users': {'total': len(users), 'official_inactive': sum(not u['is_active'] for u in users), 'e2e_users_seen_untouched': sum(u['username'].startswith('e2e-tg-') for u in users)},
    'custom_states': dict(collections.Counter(s['effective_state'] for s in states)),
    'states_pending_items': sum(s['pending_count'] for s in states),
    'disabled_backup_rows': dict(backup_summary),
    'operation_history': {'total': len(operations), 'deleted_user_rows': sum(o['username'] not in user_by_name for o in operations), 'unfinished_deleted_user_rows': sum(o['username'] not in user_by_name and o['state'] in ['deleting','delete_partial','disabling','partially_disabled','enabling','partially_enabled'] for o in operations)},
    'packages': {'total': len(packages), 'one_counted_user_auto_delete_candidates': len(single), 'candidate_package_ids': sorted(single), 'candidates_with_deletable_nonadmin_user': len(deletable_single), 'deletable_candidate_ids': sorted(deletable_single), 'candidates_with_ignored_other_inactive_users': len(ignored), 'ignored_candidate_ids': sorted(ignored), 'groups_deleted_with_candidates': sum(g['count'] for g in group_counts if g['package_id'] in single)},
    'sharing': {'credential_ref_rows': len(refs), 'cross_username_auth_groups': len(shared), 'cross_username_users': len(set().union(*shared.values())) if shared else 0, 'admin_default_shared_auth_groups': len(admin_shared), 'shared_groups_per_protocol': dict(collections.Counter(key[2] for key in shared)), 'scope': 'Exact primary-auth match among legacy, assignment, and subaccount DB refs on the same server/tag; defaults from earliest stored master_write snapshot. No runtime/Agent query; node URLs and identity-only entitlements not inferred.'},
    'sharing_with_owned_nodes': {'owned_node_matching': dict(node_counts), 'cross_username_auth_groups': len(all_shared), 'cross_username_users': len(set().union(*all_shared.values())) if all_shared else 0, 'admin_default_shared_auth_groups': len(all_admin_shared), 'scope': 'Adds owned-node parsed/clash authentication only if matching latest stored snapshot; includes SS2022 server-key validation. Empty/all-node package entitlements or subscription-time transformations are not inferred.'},
    'snapshot_evidence': {'servers': len(snapshots), 'inbounds': len(current), 'oldest_latest_snapshot': str(min((s['created_at'] for s in snapshots), default='')), 'newest_latest_snapshot': str(max((s['created_at'] for s in snapshots), default=''))},
    'actual_user_cascade_constraints': constraints,
    'shared_private_nodes': {'nonadmin_owned_nodes': sum(user_by_name.get(n['username'], {}).get('role') != 'admin' for n in node_owners), 'explicitly_referenced_by_other_bound_user_packages': len(explicit_shared_nodes), 'implicitly_referenced_via_empty_all_nodes_packages': len(implicit_shared_nodes), 'scope': 'Other active assignment or legacy binding; explicit JSON node membership separated from empty/all-node semantics.'},
    'forward_chain_owner_orphans': forward_orphans,
}
logs = subprocess.run(['journalctl', '-u', 'mmwx-custom.service', '--since', '2026-10-03 00:00:00 UTC', '-n', '10000', '--no-pager', '-o', 'cat'], text=True, capture_output=True)
patterns = ['lifecycle', 'access', 'delete_partial', 'partially_disabled', 'partially_enabled', '配置已发生漂移', '找不到', 'disabled', 'failed']
result['custom_service_logs'] = {'read_success': logs.returncode == 0, 'sample_lines': len(logs.stdout.splitlines()), 'max_lines': 10000, 'since_utc': '2026-10-03T00:00:00Z', 'matching_line_counts': {p: sum(bool(re.search(re.escape(p), line, re.I)) for line in logs.stdout.splitlines()) for p in patterns}, 'raw_lines_exported': False}
print(json.dumps(result, ensure_ascii=False, indent=2))
