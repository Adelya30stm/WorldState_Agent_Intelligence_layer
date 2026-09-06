#!/usr/bin/env python3
"""Render the README Agent World State diagram (sharp PNG, white background)."""

import math
import os

from PIL import Image, ImageDraw, ImageFont

W, H = 3200, 1400
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


F_TITLE = font("Arial Bold.ttf", 40)
F_SUB = font("Arial.ttf", 18)
F_GROUP = font("Arial Bold.ttf", 16)
F_H = font("Arial Bold.ttf", 20)
F_B = font("Arial.ttf", 15)
F_S = font("Arial.ttf", 14)
F_XS = font("Arial.ttf", 13)
F_CHIP = font("Arial Bold.ttf", 13)


def tw(draw, text, f):
    return int(draw.textbbox((0, 0), text, font=f)[2])


def rounded(draw, box, r, fill, outline, width=2):
    draw.rounded_rectangle(box, radius=r, fill=fill, outline=outline, width=width)


def wrap(draw, text, f, max_w):
    words = text.split()
    lines, cur = [], ""
    for word in words:
        trial = (cur + " " + word).strip()
        if tw(draw, trial, f) <= max_w:
            cur = trial
        else:
            if cur:
                lines.append(cur)
            cur = word
    if cur:
        lines.append(cur)
    return lines or [""]


def arrow(draw, x1, y1, x2, y2, color=LINE, width=2):
    draw.line((x1, y1, x2, y2), fill=color, width=width)
    ang = math.atan2(y2 - y1, x2 - x1)
    size = 11
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

    d.text((48, 24), "Penetration Testing  —  Agent World State", font=F_TITLE, fill=INK)
    d.text(
        (48, 76),
        "PostgreSQL is operational truth. Graphiti / Neo4j is an optional index and is not synced from world_state tables.",
        font=F_SUB,
        fill=MUTED,
    )

    ax, aw = 48, 300
    actors = [
        ("Primary agent", ORANGE, "update → researcher"),
        ("Pentester", ORANGE, "update → executor"),
        ("Tool ingest", ORANGE, "auto → executor"),
        ("Human", WHITE, "REST candidate review"),
        ("System", WHITE, "link journal only"),
    ]
    ah, gap = 76, 14
    total_h = len(actors) * ah + (len(actors) - 1) * gap
    ay0 = 168
    d.text((ax, ay0 - 32), "Who writes  (via Store)", font=F_GROUP, fill=MUTED)
    for i, (name, pal, sub) in enumerate(actors):
        agent_box(d, ax, ay0 + i * (ah + gap), aw, ah, name, pal, sub)
    d.text((ax + aw / 2, ay0 + total_h + 14), "developer actor unused", font=F_XS, fill=MUTED, anchor="mt")

    spine_x = ax + aw + 18
    first_mid = ay0 + ah / 2
    last_mid = ay0 + total_h - ah / 2
    actor_mid = (first_mid + last_mid) / 2
    d.line((spine_x, first_mid, spine_x, last_mid), fill=ORANGE[1], width=3)
    for i in range(len(actors)):
        y = ay0 + i * (ah + gap) + ah / 2
        d.line((ax + aw, y, spine_x, y), fill=ORANGE[1], width=2)

    cx, cy, cw, ch = 420, 128, 1020, 1000
    rounded(d, (cx, cy, cx + cw, cy + ch), 16, (255, 252, 242), YELLOW[1], 2)
    d.text((cx + 24, cy + 14), "WORLD STATE CORE", font=F_GROUP, fill=YELLOW[1])

    lx, ly, lw, lh = cx + 24, cy + 48, cw - 48, 220
    rounded(d, (lx, ly, lx + lw, ly + lh), 12, YELLOW[0], YELLOW[1], 2)
    d.text((lx + 20, ly + 12), "Lifecycle State Machine", font=F_H, fill=INK)
    states = ["unknown", "discovered", "scanning", "assessed", "vulnerable", "exploited", "remediated"]
    chip_h = 34
    chip_y = ly + 56
    arrow_w = 18
    widths = [tw(d, st, F_CHIP) + 22 for st in states]
    total_chips = sum(widths) + (len(states) - 1) * arrow_w
    xs = lx + 20 + max(0, (lw - 40 - total_chips) / 2)
    for i, st in enumerate(states):
        cw_chip = widths[i]
        rounded(d, (xs, chip_y, xs + cw_chip, chip_y + chip_h), 8, BG, YELLOW[1], 2)
        d.text((xs + cw_chip / 2, chip_y + chip_h / 2), st, font=F_CHIP, fill=INK, anchor="mm")
        if i < len(states) - 1:
            arrow(d, xs + cw_chip + 2, chip_y + chip_h / 2, xs + cw_chip + arrow_w - 2, chip_y + chip_h / 2, YELLOW[1])
        xs += cw_chip + arrow_w
    d.text((lx + 20, ly + lh - 68), "ValidateTransition()", font=F_H, fill=INK)
    d.text(
        (lx + 20, ly + lh - 38),
        "allowed transitions only  ·  rejected → error  ·  skips: discovered→assessed, assessed|vulnerable→remediated",
        font=F_S,
        fill=MUTED,
    )

    wx, wy, ww, wh = cx + 24, ly + lh + 18, cw - 48, 400
    rounded(d, (wx, wy, wx + ww, wy + wh), 12, BG, YELLOW[1], 2)
    d.text((wx + 20, wy + 14), "pkg/worldstate", font=F_H, fill=INK)
    d.text(
        (wx + 20, wy + 44),
        "extract / ingest / store   ·   REST WorldStateService is UI-only and does not commit",
        font=F_S,
        fill=MUTED,
    )
    bullets = [
        "extract entities  (hosts, IPs, URLs, services, findings, credentials)",
        "build links  ·  deduplicate",
        "commit entity + link + transition + journal event in one transaction",
        "build snapshot projection  (what is true now)",
        "validate lifecycle before any state UPDATE",
    ]
    by = wy + 86
    for b in bullets:
        d.ellipse((wx + 24, by + 6, wx + 32, by + 14), fill=YELLOW[1])
        d.text((wx + 42, by), b, font=F_B, fill=INK)
        by += 36

    sx, sy, sw, sh = cx + 24, wy + wh + 18, cw - 48, 126
    rounded(d, (sx, sy, sx + sw, sy + sh), 12, BLUE[0], BLUE[1], 2)
    d.text((sx + sw / 2, sy + 42), "Snapshot", font=F_H, fill=INK, anchor="mm")
    d.text((sx + sw / 2, sy + 76), "what is true now  ·  projection, not a live Graphiti query", font=F_S, fill=MUTED, anchor="mm")

    px, py, pw, ph = 1488, 128, 560, 1000
    rounded(d, (px, py, px + pw, py + ph), 16, (245, 250, 253), BLUE[1], 2)
    d.text((px + 24, py + 14), "PERSISTENCE", font=F_GROUP, fill=BLUE[1])
    d.text((px + pw - 24, py + 16), "PostgreSQL", font=F_GROUP, fill=BLUE[1], anchor="rt")

    tables = [
        (BLUE, "world_state_entities", "what exists + lifecycle"),
        (BLUE, "world_state_links", "HAS_SERVICE / HAS_FINDING"),
        (BLUE, "world_state_transitions", "audit trail"),
        (YELLOW, "world_state_events", "append-only Event Journal"),
        (BLUE, "world_state_chain_cursors", "msgchain_id → revision"),
        (BLUE, "agent_chain_waits", "human vs World State wake"),
        (WHITE, "msglogs / termlogs / toolcalls", "execution history  ·  not World State"),
    ]
    ty = py + 52
    th = 124
    for pal, title, sub in tables:
        cylinder(d, px + 28, ty, pw - 56, th, pal[0], pal[1], title, sub)
        ty += th + 8

    rx, rw = 2092, 1060
    agent_box(d, rx, 128, rw, 140, "Graphiti / Neo4j", GREEN, "optional semantic index  ·  not synced from world_state_*")
    rounded(d, (rx, 292, rx + rw, 500), 12, YELLOW[0], YELLOW[1], 2)
    d.text((rx + 24, 310), "World State Delivery", font=F_H, fill=INK)
    d.text((rx + 24, 348), "Primary agent only  ·  next model-turn boundary", font=F_B, fill=MUTED)
    d.text((rx + 24, 384), "no cursor → baseline     cursor behind, fits limits → delta", font=F_S, fill=INK)
    d.text((rx + 24, 412), "over 64 events / 64 KiB / 128 entities|links → checkpoint", font=F_S, fill=INK)
    d.text((rx + 24, 440), "cursor == head → none     envelope + cursor advance in one TX", font=F_S, fill=INK)

    agent_box(d, rx, 524, rw, 140, "Primary Agent", ORANGE, "receives <world_state> before the next model turn")
    rounded(d, (rx, 688, rx + rw, 980), 12, YELLOW[0], YELLOW[1], 2)
    d.text((rx + 24, 708), "Attack Planner", font=F_H, fill=INK)
    d.text((rx + 24, 748), "journal + snapshot evidence  ·  not a live Graphiti query", font=F_B, fill=MUTED)
    d.text((rx + 24, 790), "pkg/attackplan is not on the live tool loop.", font=F_S, fill=INK)
    d.text((rx + 24, 822), "Next action: tool, subtask, or durable ask wait.", font=F_S, fill=INK)
    d.text((rx + 24, 854), "Execution result returns through extract / ingest.", font=F_S, fill=INK)

    arrow(d, spine_x, actor_mid, cx - 8, actor_mid, ORANGE[1], 3)
    label(d, (spine_x + cx) / 2, actor_mid - 18, "via Store", ORANGE[1])

    svc_y = wy + 150
    arrow(d, cx + cw + 4, svc_y, px - 4, svc_y, BLUE[1], 3)
    label(d, (cx + cw + px) / 2, svc_y - 18, "commit 1 TX", BLUE[1])

    events_y = py + 52 + 3 * (124 + 8) + 62
    arrow(d, cx + cw + 4, wy + 230, px - 4, events_y, PURPLE, 2)
    label(d, (cx + cw + px) / 2, events_y + 20, "append-only", PURPLE)

    arrow(d, px + pw + 4, events_y, rx - 4, 390, PURPLE, 2)
    label(d, (px + pw + rx) / 2, 330, "revision N+1…M", PURPLE)

    arrow(d, rx + rw / 2, 500, rx + rw / 2, 524, ORANGE[1], 2)
    label(d, rx + rw / 2 + 110, 512, "delta / checkpoint", ORANGE[1])

    cursors_y = py + 52 + 4 * (124 + 8) + 62
    arrow(d, px + pw + 4, cursors_y, rx - 4, 590, BLUE[1], 2)
    label(d, (px + pw + rx) / 2, 560, "cursor", BLUE[1])

    arrow(d, rx + rw / 2, 664, rx + rw / 2, 688, ORANGE[1], 2)
    label(d, rx + rw / 2 + 48, 676, "replan", ORANGE[1])

    bypass_y = 1138
    arrow(d, sx + sw / 2, sy + sh, sx + sw / 2, bypass_y, GREEN[1], 2)
    arrow(d, sx + sw / 2, bypass_y, rx + 28, bypass_y, GREEN[1], 2)
    arrow(d, rx + 28, bypass_y, rx + 28, 980, GREEN[1], 2)
    label(d, (sx + sw / 2 + rx + 28) / 2, bypass_y, "verified evidence", GREEN[1])

    legend_y = 1228
    d.text((48, legend_y), "Legend", font=F_GROUP, fill=MUTED)
    legend_swatch(d, 48, legend_y + 30, ORANGE, "Agents")
    legend_swatch(d, 180, legend_y + 30, YELLOW, "Logic / core")
    legend_swatch(d, 360, legend_y + 30, BLUE, "Storage")
    legend_swatch(d, 510, legend_y + 30, GREEN, "Optional")
    legend_swatch(d, 680, legend_y + 30, WHITE, "Data / support")
    d.text(
        (48, legend_y + 68),
        "Closed loop: mutation + journal in one TX  →  delivery to Primary  →  next action  →  extract/ingest  →  mutation.  In-flight LLM calls are not interrupted.",
        font=F_S,
        fill=MUTED,
    )
    d.text(
        (48, legend_y + 96),
        "Source: backend/pkg/worldstate  ·  world_state_delivery.go  ·  lifecycle.go  ·  Sep 2026",
        font=F_XS,
        fill=(150, 150, 154),
    )

    img.save(OUT, "PNG", dpi=(192, 192), compress_level=4)
    print(OUT, img.size)


if __name__ == "__main__":
    main()
