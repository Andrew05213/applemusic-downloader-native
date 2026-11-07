# Application icon

- `app-icon.ico`: Windows application/installer icon.
- `app-icon.png`: canonical 256px artwork for application windows and UI branding,
  converted from the built-in imagegen export with the existing packaging tool.

The larger original `app-icon-master.png` is a local-only generation export,
excluded from Git. A fresh clone needs neither that file nor an image-generation
account to build or regenerate the committed Windows icon.

Generate the Windows icon format without altering the artwork:

```sh
node scripts/prepare-app-icon.cjs
```

Prompt: Minimal square application icon, solid red background, exactly one white
eighth note, centered, no text, lettering, extra symbols, border, shadows or 3D.
