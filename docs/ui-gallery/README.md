# Xirang UI Gallery

Static HTML reference for Sage design-system primitives. Each file is self-contained and can be opened directly in a browser.

## Primitives

| File | Component | Description |
|------|-----------|-------------|
| [buttons.html](./buttons.html) | `<Button>` | All variants, sizes, shapes, loading states |
| [badges.html](./badges.html) | `<Badge>` | All tones (success / warning / destructive / info / neutral) |
| [cards.html](./cards.html) | `<Card>` | Card + CardHeader + CardContent + CardTitle |
| [dialog.html](./dialog.html) | `<Dialog>` | Modal dialog layout reference |

## Design tokens (CSS variables)

All primitives rely on CSS custom properties defined in `web/src/index.css`.  
Light / dark modes are toggled via `class="dark"` on `<html>`.

## Usage

Open any `.html` file directly — no build step required.  
The files include a light/dark toggle button in the top-right corner.
