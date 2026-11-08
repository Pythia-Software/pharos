# Query Table handoff: use the arrow for disabled picker items

Status: upstream change requested; no local Query Table patch.
Baseline: published Query Table 1.0.3 / Go v1.0.3.
Consumer: Pharos. Prepared 2026-11-08.

Pharos is simplifying its cursor usage. Disabled controls should use the
ordinary arrow. Pharos-owned styles now use `default` for disabled and busy
controls; the published Query Table theme still requests `not-allowed` for
disabled picker items.

In `@pythia-software/query-table-ui` 1.0.3, `dist/theme.css` has:

```css
.qt-picker-item--disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
```

Change the corresponding authored theme rule upstream to `cursor: default`.
This explicit value is needed because `.qt-picker-item` requests `pointer`.
The rule is used by the `FieldPicker` component, which adds
`qt-picker-item--disabled` when a field is unavailable.

Preserve disabled-item opacity, explanations, event handling, and keyboard
behavior. Keep enabled items clickable with the normal hand cursor. Verify in
a browser that an unavailable picker item shows the arrow and remains
unavailable, and that an enabled item can still be selected. No cursor images,
consumer-specific override, or new API is needed.

Publish a Query Table release containing the theme change. Pharos can then
adopt it by pinning all four npm packages and the Go module to the same
release and rebuilding the frontend. Until then the declaration remains in
the generated main-app and feedback-inbox bundles. Browser inspection confirms
that both hosts' existing generic disabled-button rules take precedence and
already give these picker buttons the arrow; the upstream theme alone still
shows `not-allowed`.
