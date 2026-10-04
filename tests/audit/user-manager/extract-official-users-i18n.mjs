import fs from 'fs';
const s = fs.readFileSync('/tmp/mmwx-sync/fe-v0.5.5/assets/index-DKBDDJNu.js', 'utf8');
function objAt(i) { // s[i] === '{'
  let depth = 0, q = null;
  for (let j = i; j < s.length; j++) {
    const c = s[j];
    if (q) { if (c === '\\') { j++; continue; } if (c === q) q = null; continue; }
    if (c === "'" || c === '"' || c === '`') { q = c; continue; }
    if (c === '{') depth++;
    else if (c === '}') { depth--; if (depth === 0) return s.slice(i, j + 1); }
  }
}
function defOf(name) {
  const re = new RegExp(`[,;\\s]${name.replace('$','\\$')}=\\{`, 'g');
  const m = re.exec(s); if (!m) return null;
  return objAt(m.index + m[0].length - 1);
}
const cache = {};
function resolve(name) {
  if (cache[name]) return cache[name];
  const src = defOf(name); if (!src) return undefined;
  const ids = [...new Set([...src.matchAll(/:([A-Za-z_$][\w$]*)(?=[,}])/g)].map(m => m[1]))].filter(x => !['true','false','null'].includes(x));
  const scope = {}; for (const id of ids) scope[id] = resolve(id);
  const fn = new Function(...Object.keys(scope), `return (${src});`);
  return cache[name] = fn(...Object.values(scope));
}
// find zh root: object that contains 'nodes':<var> where var has Chinese
const out = {};
for (const m of s.matchAll(/'(\w+)':([A-Za-z_$][\w$]*)(?=[,}])/g)) {}
const rootName = process.argv[2];
const root = resolve(rootName);
fs.writeFileSync('/root/mmwx-custom-artifacts/user-manager-audit/official/zh-' + rootName + '.json', JSON.stringify(root, null, 1));
console.log(Object.keys(root));

if(rootName==='Sx'){const flat={};function flatten(o,p=''){for(const[k,v]of Object.entries(o)){const n=p?p+'.'+k:k;if(v&&typeof v==='object')flatten(v,n);else flat[n]=v;}}flatten(root);Object.assign(flat,resolve('Wx').users);fs.writeFileSync('/root/mmwx-custom-artifacts/user-manager-audit/official/zh-users-flat.json',JSON.stringify(flat,null,2)+'\n');}
