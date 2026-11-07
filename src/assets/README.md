# Application icon

- `app-icon-master.png`: original artwork generated with the built-in imagegen tool.
- `app-icon.ico`: Windows application/installer icon.
- `app-icon.png`: 256px copy for application windows and UI branding.

Generate the Windows icon format without altering the artwork:

```sh
node scripts/prepare-app-icon.cjs
```

Prompt: Minimal square application icon, solid red background, exactly one white
eighth note, centered, no text, lettering, extra symbols, border, shadows or 3D.
