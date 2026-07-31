# Visual Regression

This folder stores screenshot baselines, current captures, generated diffs, and the HTML report for mmwx-custom UI checks.

## Layout

- `baseline/`: approved screenshots. Do not update automatically.
- `current/`: screenshots from the latest run.
- `diff/`: generated visual diffs.
- `report/index.html`: generated HTML report.
- `visual.mjs`: Playwright runner.

The initial coverage includes:

- Dashboard: 360px, 375px, 390px, 412px.
- Service management grid: 360px, 375px, 390px, 412px.
- Service management list: 360px, 375px, 390px, 412px.

Placeholder folders are present for future pages:

- `login/`
- `nodes/`
- `users/`
- `settings/`
- `reports/`

## Run

From `frontend/`:

```bash
MMWXC_USERNAME=... MMWXC_PASSWORD=... npm run visual
```

Optional environment:

- `VISUAL_BASE_URL`: defaults to `https://mmwxc.imgamer.top`.
- `VISUAL_SKIP_BUILD=1`: skip the build step.

The runner performs:

1. `npm run build`
2. Browser login
3. Dashboard screenshots
4. Service grid screenshots
5. Service list screenshots
6. Baseline/current comparison
7. Diff image generation
8. HTML report generation

Screenshots use the mobile viewport rather than full-page capture. The runner also disables live WebSocket/SSE updates inside the test browser so real-time metrics do not create noisy diffs.

## Update Baseline

Only update baseline after the new UI has been reviewed and explicitly approved:

```bash
MMWXC_USERNAME=... MMWXC_PASSWORD=... npm run visual -- --update-baseline
```

Do not use `--update-baseline` during normal checks.

## Add Pages

Add a page by editing `tests/visual/visual.mjs`:

1. Add a capture function that navigates to the page.
2. Add an entry to the `pages` array.
3. Add a matching folder under `baseline/`, `current/`, and `diff/`.
4. Create baseline screenshots only after review approval.
