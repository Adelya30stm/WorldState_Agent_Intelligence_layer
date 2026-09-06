#!/usr/bin/env python3
"""Render the README Agent World State diagram (sharp PNG, white background)."""

import math
import os

from PIL import Image, ImageDraw, ImageFont

W, H = 3600, 1680
OUT = os.path.join(os.path.dirname(__file__), "agent-world-state.png")

BG = (255, 255, 255)
INK = (28, 28, 30)
MUTED = (92, 92, 96)
LINE = (72, 72, 76)

ORANGE = ((255, 236, 217), (224, 122, 58))
YELLOW = ((255, 249, 230), (196, 160, 40))
BLUE = ((232, 244, 252), (46, 116, 168))
GREEN = ((232, 245, 233), (46, 125, 82))
WHITE = ((255, 255, 255), (120, 120, 125))
PURPLE = (123, 62, 155)

FONT_DIR = "/System/Library/Fonts/Supplemental"


def font(name, size):
    return ImageFont.truetype(os.path.join(FONT_DIR, name), size)


F_TITLE = font("Arial Bold.ttf", 44)
F_SUB = font("Arial.ttf", 20)
F_GROUP = font("Arial Bold.ttf", 18)
F_H = font("Arial Bold.ttf", 22)
F_B = font("Arial.ttf", 16)
F_S = font("Arial.ttf", 15)
F_XS = font("Arial.ttf", 14)
F_CHIP = font("Arial Bold.ttf", 14)


def tw(draw, text, f):
    return int(draw.textbbox((0, 0), text, font=f)[2])


def rounded(draw, box, r, fill, outline, width=2):
    draw.rounded_rectangle(box, radius=r, fill=fill, outline=outline, width=width)


def arrow(draw, x1, y1, x2, y2, color=LINE, width=2):
    draw.line((x1, y1, x2, y2), fill=color, width=width)
    ang = math.atan2(y2 - y1, x2 - x1)
    size = 12
    draw.polygon(
        [
            (x2, y2),
            (x2 - size * math.cos(ang - 0.42), y2 - size * math.sin(ang - 0.42)),
            (x2 - size * math.cos(ang + 0.42), y2 - size * math.sin(ang + 0.42)),
        ],
        fill=color,
    )


def label(draw, x, y, text, color=MUTED, bg=BG):
    w = tw(draw, text, F_XS)
    draw.rectangle((x - w / 2 - 6, y - 10, x + w / 2 + 6, y + 10), fill=bg)
    draw.text((x, y), text, font=F_XS, fill=color, anchor="mm")


def cylinder(draw, x, y, w, h, fill, outline, title, sub=None):
    eh = 18
    draw.ellipse((x, y + h - eh, x + w, y + h), fill=fill, outline=outline, width=2)
    draw.rectangle((x, y + eh // 2, x + w, y + h - eh // 2), fill=fill, outline=None)
    draw.line((x, y + eh // 2, x, y + h - eh // 2), fill=outline, width=2)
    draw.line((x + w, y + eh // 2, x + w, y + h - eh // 2), fill=outline, width=2)
    draw.ellipse((x, y, x + w, y + eh), fill=fill, outline=outline, width=2)
    draw.text((x + w / 2, y + h / 2 - (6 if sub else 0)), title, font=F_H, fill=INK, anchor="mm")
    if sub:
        draw.text((x + w / 2, y + h / 2 + 16), sub, font=F_XS, fill=MUTED, anchor="mm")


def agent_box(draw, x, y, w, h, title, pal, sub=None):
    rounded(draw, (x, y, x + w, y + h), 12, pal[0], pal[1], 2)
    draw.text((x + w / 2, y + h / 2 - (8 if sub else 0)), title, font=F_H, fill=INK, anchor="mm")
    if sub:
        draw.text((x + w / 2, y + h / 2 + 14), sub, font=F_XS, fill=MUTED, anchor="mm")


def legend_swatch(draw, x, y, pal, name):
    rounded(draw, (x, y, x + 22, y + 16), 4, pal[0], pal[1], 2)
    draw.text((x + 30, y + 8), name, font=F_XS, fill=INK, anchor="lm")


def main():
    img = Image.new("RGB", (W, H), BG)
    d = ImageDraw.Draw(img)

    d.text((56, 28), "Penetration Testing  —  Agent World State", font=F_TITLE, fill=INK)
    d.text(
        (56, 84),
        "PostgreSQL is operational truth. Graphiti / Neo4j is optional and is not synced from world_state tables.",
        font=F_SUB,
        fill=MUTED,
    )

    ax, aw, ah, gap = 56, 320, 88, 18
    actors = [
        ("Primary agent", ORANGE, "world_state_update  →  researcher"),
        ("Executor", ORANGE, "tool ingest + pentester update"),
        ("Human", WHITE, "REST candidate review"),
        ("System", WHITE, "link journal only"),
    ]
    total_h = len(actors) * ah + (len(actors) - 1) * gap
    ay0 = 210
    d.text((ax, ay0 - 36), "Write sources", font=F_GROUP, fill=MUTED)
    for i, (name, pal, sub) in enumerate(actors):
        agent_box(d, ax, ay0 + i * (ah + gap), aw, ah, name, pal, sub)
    d.text((ax + aw / 2, ay0 + total_h + 16), "via pkg/worldstate.Store", font=F_XS, fill=MUTED, anchor="mt")

    spine_x = ax + aw + 20
    first_mid = ay0 + ah / 2
    last_mid = ay0 + total_h - ah / 2
    actor_mid = (first_mid + last_mid) / 2
    d.line((spine_x, first_mid, spine_x, last_mid), fill=ORANGE[1], width=3)
    for i in range(len(actors)):
        y = ay0 + i * (ah + gap) + ah / 2
        d.line((ax + aw, y, spine_x, y), fill=ORANGE[1], width=2)

    cx, cy, cw, ch = 460, 150, 1180, 1080
    rounded(d, (cx, cy, cx + cw, cy + ch), 18, BG, YELLOW[1], 3)
    d.text((cx + 28, cy + 16), "WORLD STATE CORE", font=F_GROUP, fill=YELLOW[1])

    lx, ly, lw, lh = cx + 28, cy + 56, cw - 56, 250
    rounded(d, (lx, ly, lx + lw, ly + lh), 14, YELLOW[0], YELLOW[1], 2)
    d.text((lx + 24, ly + 16), "Lifecycle State Machine", font=F_H, fill=INK)
    states = ["unknown", "discovered", "scanning", "assessed", "vulnerable", "exploited", "remediated"]
    chip_h, chip_y, arrow_w = 38, ly + 64, 20
    widths = [tw(d, st, F_CHIP) + 24 for st in states]
    total_chips = sum(widths) + (len(states) - 1) * arrow_w
    xs = lx + 24 + max(0, (lw - 48 - total_chips) / 2)
    for i, st in enumerate(states):
        cw_chip = widths[i]
        rounded(d, (xs, chip_y, xs + cw_chip, chip_y + chip_h), 10, BG, YELLOW[1], 2)
        d.text((xs + cw_chip / 2, chip_y + chip_h / 2), st, font=F_CHIP, fill=INK, anchor="mm")
        if i < len(states) - 1:
            arrow(d, xs + cw_chip + 2, chip_y + chip_h / 2, xs + cw_chip + arrow_w - 2, chip_y + chip_h / 2, YELLOW[1])
        xs += cw_chip + arrow_w
    d.text((lx + 24, ly + lh - 72), "ValidateTransition()", font=F_H, fill=INK)
    d.text((lx + 24, ly + lh - 40), "allowed transitions only  ·  rejected → error", font=F_S, fill=MUTED)

    wx, wy, ww, wh = cx + 28, ly + lh + 20, cw - 56, 430
    rounded(d, (wx, wy, wx + ww, wy + wh), 14, BG, YELLOW[1], 2)
    d.text((wx + 24, wy + 16), "pkg/worldstate", font=F_H, fill=INK)
    d.text((wx + 24, wy + 48), "extract / ingest / store   ·   REST WorldStateService is UI-only", font=F_S, fill=MUTED)
    bullets = [
        "extract entities (hosts, IPs, URLs, services, findings, credentials)",
        "build links  ·  deduplicate",
        "commit entity + link + transition + journal event in one transaction",
        "build snapshot projection (what is true now)",
        "validate lifecycle before any state UPDATE",
    ]
    by = wy + 96
    for b in bullets:
        d.ellipse((wx + 28, by + 7, wx + 38, by + 17), fill=YELLOW[1])
        d.text((wx + 50, by), b, font=F_B, fill=INK)
        by += 40

    sx, sy, sw, sh = cx + 28, wy + wh + 20, cw - 56, 140
    rounded(d, (sx, sy, sx + sw, sy + sh), 14, BLUE[0], BLUE[1], 2)
    d.text((sx + sw / 2, sy + 48), "Snapshot", font=F_H, fill=INK, anchor="mm")
    d.text((sx + sw / 2, sy + 86), "what is true now", font=F_S, fill=MUTED, anchor="mm")

    px, py, pw, ph = 1700, 150, 620, 1080
    rounded(d, (px, py, px + pw, py + ph), 18, BG, BLUE[1], 3)
    d.text((px + 28, py + 16), "PERSISTENCE", font=F_GROUP, fill=BLUE[1])
    d.text((px + pw - 28, py + 18), "PostgreSQL", font=F_GROUP, fill=BLUE[1], anchor="rt")
    tables = [
        (BLUE, "world_state_entities", "what exists + lifecycle"),
        (BLUE, "world_state_links", "HAS_SERVICE / HAS_FINDING"),
        (BLUE, "world_state_transitions", "audit trail"),
        (YELLOW, "world_state_events", "append-only Event Journal"),
        (BLUE, "world_state_chain_cursors", "msgchain_id → revision"),
        (BLUE, "agent_chain_waits", "human vs World State wake"),
        (WHITE, "msglogs / termlogs / toolcalls", "execution history · not World State"),
    ]
    ty, th = py + 56, 134
    for pal, title, sub in tables:
        cylinder(d, px + 32, ty, pw - 64, th, pal[0], pal[1], title, sub)
        ty += th + 10

    rx, rw = 2380, 1160
    agent_box(d, rx, 150, rw, 150, "Graphiti / Neo4j", GREEN, "optional  ·  not synced from world_state_*")
    rounded(d, (rx, 326, rx + rw, 560), 14, YELLOW[0], YELLOW[1], 2)
    d.text((rx + 28, 348), "World State Delivery", font=F_H, fill=INK)
    d.text((rx + 28, 392), "Primary only  ·  next model-turn boundary", font=F_B, fill=MUTED)
    d.text((rx + 28, 432), "no cursor → baseline     cursor behind → delta", font=F_S, fill=INK)
    d.text((rx + 28, 466), "over limits → checkpoint     cursor == head → none", font=F_S, fill=INK)
    d.text((rx + 28, 500), "envelope + cursor advance in one TX", font=F_S, fill=INK)

    agent_box(d, rx, 586, rw, 150, "Primary Agent", ORANGE, "receives <world_state> before the next model turn")
    rounded(d, (rx, 762, rx + rw, 1080), 14, YELLOW[0], YELLOW[1], 2)
    d.text((rx + 28, 788), "Next action", font=F_H, fill=INK)
    d.text((rx + 28, 832), "tool, subtask, or durable ask wait", font=F_B, fill=MUTED)
    d.text((rx + 28, 876), "performPlanner still calls adviser.", font=F_S, fill=INK)
    d.text((rx + 28, 910), "pkg/attackplan is not on the live tool loop.", font=F_S, fill=INK)
    d.text((rx + 28, 944), "Execution result returns through extract / ingest.", font=F_S, fill=INK)

    arrow(d, spine_x, actor_mid, cx - 10, actor_mid, ORANGE[1], 3)
    label(d, (spine_x + cx) / 2, actor_mid - 20, "via Store", ORANGE[1])

    svc_y = wy + 160
    arrow(d, cx + cw + 6, svc_y, px - 6, svc_y, BLUE[1], 3)
    label(d, (cx + cw + px) / 2, svc_y - 20, "commit 1 TX", BLUE[1])

    events_y = py + 56 + 3 * (134 + 10) + 67
    arrow(d, cx + cw + 6, wy + 250, px - 6, events_y, PURPLE, 3)
    label(d, (cx + cw + px) / 2, events_y + 22, "append-only", PURPLE)

    arrow(d, px + pw + 6, events_y, rx - 6, 430, PURPLE, 3)
    label(d, (px + pw + rx) / 2, 370, "revision N+1…M", PURPLE)

    arrow(d, rx + rw / 2, 560, rx + rw / 2, 586, ORANGE[1], 3)
    label(d, rx + rw / 2 + 120, 573, "delta / checkpoint", ORANGE[1])

    cursors_y = py + 56 + 4 * (134 + 10) + 67
    arrow(d, px + pw + 6, cursors_y, rx - 6, 650, BLUE[1], 3)
    label(d, (px + pw + rx) / 2, 620, "cursor", BLUE[1])

    arrow(d, rx + rw / 2, 736, rx + rw / 2, 762, ORANGE[1], 3)

    legend_y = 1290
    d.text((56, legend_y), "Legend", font=F_GROUP, fill=MUTED)
    legend_swatch(d, 56, legend_y + 34, ORANGE, "Agents")
    legend_swatch(d, 200, legend_y + 34, YELLOW, "Logic / core")
    legend_swatch(d, 390, legend_y + 34, BLUE, "Storage")
    legend_swatch(d, 550, legend_y + 34, GREEN, "Optional")
    legend_swatch(d, 730, legend_y + 34, WHITE, "Data / support")
    d.text(
        (56, legend_y + 78),
        "Closed loop: mutation + journal in one TX  →  delivery to Primary  →  next action  →  extract/ingest  →  mutation.",
        font=F_S,
        fill=MUTED,
    )

    img.save(OUT, "PNG", dpi=(192, 192), compress_level=3)
    print(OUT, img.size)


if __name__ == "__main__":
    main()
