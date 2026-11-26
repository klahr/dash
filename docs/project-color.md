# Project color

Give each project a color, so you see at a glance which one is open.

## Choose a color

1. Open **Project settings → General**.
2. Under **Project color**, pick a swatch, or the last one to choose any other color.
   **None** takes the color away.

The color marks the line under the top bar, the project's dot and icon, and the browser's
toolbar on a phone.

## Buttons and highlights

With **Use the project color for buttons and highlights** on (the default), the color also
goes on primary buttons, **New task**, links, focus rings, checkboxes and radios, and the
selected item in lists, tabs and the nav.

Dash adjusts the color so it reads on the dark page:

- A dark color is lightened until it stands out from the page and the cards (at least 3:1).
- Text on a filled button stays readable (at least 4.5:1).

Turn the setting off to keep the color on the top bar, dot and icon only. Buttons then use
Dash's default blue.

The default brand color is used when no project is open, for a project without a color, and
on **Planning**, which is across projects. The chat's project tabs use the open project's
color.

## Status colors stay the same

The colors of task states never change: queued (blue), running (yellow), review (teal),
merged (green) and failed (red). Toasts, usage meters, drop zones and syntax highlighting keep
their colors too. A project in red, yellow or teal still has buttons that look like buttons:
they are filled shapes with a label, while a state is a dot or a stripe on a card.

From a chat or the MCP connector, `update_project` sets it as `color_buttons` (`true` or
`false`).
