#!/usr/bin/env python3
"""Render the current World State architecture diagram to PNG."""

import os
import sys

from PIL import Image, ImageDraw, ImageFont

W, H = 2720, 1860
OUT = os.path.join(os.path.dirname(__file__), "agent-world-state-architecture.png")

BG = (250, 250, 248)
INK = (28, 28, 30)
MUTED = (90, 90, 95)
LINE = (70, 70, 75)

ORANGE = ((255, 236, 217), (224, 122, 58))
GRAY = ((245, 245, 245), (120, 120, 125))
YELLOW = ((255, 249, 230), (196, 160, 40))
BLUE = ((232, 244, 252), (46, 116, 168))
GREEN = ((232, 245, 233), (46, 125, 82))
PURPLE = ((243, 232, 250), (123, 62, 155))
WHITE = ((255, 255, 255), (90, 90, 95))
PINK = ((255, 236, 239), (176, 70, 86))

FONT_DIR = "/System/Library/Fonts/Supplemental"


def font(name, size):
    path = os.path.join(FONT_DIR, name)
    return ImageFont.truetype(path, size)


F_TITLE = font("Arial Bold.ttf", 34)
F_SUB = font("Arial.ttf", 16)
F_H = font("Arial Bold.ttf", 17)
F_B = font("Arial.ttf", 13)
F_S = font("Arial.ttf", 12)
F_XS = font("Arial.ttf", 11)
F_CHIP = font("Arial Bold.ttf", 11)


def rounded(draw, box, r, fill, outline, width=2):
    draw.rounded_rectangle(box, radius=r, fill=fill, outline=outline, width=width)


def text_w(draw, text, f):
    return int(draw.textbbox((0, 0), text, font=f)[2])


def wrap(draw, text, f, max_w):
    words = text.split()
    lines, cur = [], ""
    for w in words:
        trial = (cur + " " + w).strip()
        if text_w(draw, trial, f) <= max_w:
            cur = trial
        else:
            if cur:
                lines.append(cur)
            cur = w
    if cur:
        lines.append(cur)
    return lines or [""]


def draw_text(draw, xy, text, f, fill=INK, anchor="lt"):
    draw.text(xy, text, font=f, fill=fill, anchor=anchor)


def box(draw, x, y, w, h, title, fill, outline, body=None, chips=None, footer=None):
    rounded(draw, (x, y, x + w, y + h), 10, fill, outline, 2)
    draw_text(draw, (x + 14, y + 10), title, F_H, INK)
    cy = y + 36
    if body:
        for line in body:
            for wrapped in wrap(draw, line, F_S, w - 28):
                draw_text(draw, (x + 14, cy), wrapped, F_S, MUTED)
                cy += 16
            cy += 4
    if chips:
        cx, row_y = x + 14, cy
        for chip in chips:
            tw = text_w(draw, chip, F_CHIP) + 16
            if cx + tw > x + w - 12:
                cx = x + 14
                row_y += 26
            rounded(draw, (cx, row_y, cx + tw, row_y + 20), 6, (255, 255, 255), outline, 1)
            draw_text(draw, (cx + tw / 2, row_y + 10), chip, F_CHIP, outline, anchor="mm")
            cx += tw + 6
        cy = row_y + 28
    if footer:
        for line in footer:
            for wrapped in wrap(draw, line, F_XS, w - 28):
                draw_text(draw, (x + 14, cy), wrapped, F_XS, MUTED)
                cy += 14
    return cy


def arrow(draw, x1, y1, x2, y2, label=None, dashed=False):
    if dashed:
        draw.line((x1, y1, x2, y2), fill=LINE, width=2)
    else:
        draw.line((x1, y1, x2, y2), fill=LINE, width=2)
    # head
    import math

    ang = math.atan2(y2 - y1, x2 - x1)
    l = 10
    draw.polygon(
        [
            (x2, y2),
            (x2 - l * math.cos(ang - 0.4), y2 - l * math.sin(ang - 0.4)),
            (x2 - l * math.cos(ang + 0.4), y2 - l * math.sin(ang + 0.4)),
        ],
        fill=LINE,
    )
    if label:
        mx, my = (x1 + x2) / 2, (y1 + y2) / 2 - 8
        tw = text_w(draw, label, F_XS)
        draw.rectangle((mx - tw / 2 - 4, my - 8, mx + tw / 2 + 4, my + 8), fill=BG)
        draw_text(draw, (mx, my), label, F_XS, PINK[1], anchor="mm")


def h_arrow(draw, x1, x2, y, label):
    arrow(draw, x1, y, x2, y, label)


def main():
    img = Image.new("RGB", (W, H), BG)
    d = ImageDraw.Draw(img)

    d.text((40, 24), "Agent World State — Storage + Runtime Feedback Loop", font=F_TITLE, fill=INK)
    d.text(
        (40, 68),
        "Current RedScope / PentAGI implementation. PostgreSQL is operational truth. Graphiti is optional and is not synced from world_state tables.",
        font=F_SUB,
        fill=MUTED,
    )

    # --- Agents ---
    box(
        d, 40, 110, 360, 620,
        "Runtime agents  (msgchain types)",
        *ORANGE,
        body=["Primary is the only chain that receives World State delivery. Specialists do not."],
        chips=[
            "primary_agent",
            "assistant",
            "generator",
            "refiner",
            "reporter",
            "pentester",
            "coder",
            "installer",
            "searcher",
            "enricher",
            "memorist",
            "adviser",
            "reflector",
            "summarizer",
            "tool_call_fixer",
            "web_reporter",
        ],
        footer=[
            "Journal actor strings (not LLM types): researcher, developer, executor, human, system.",
            "Human participates via ask + agent_chain_waits.",
            "web_* prompt types exist; only web_reporter has a dedicated performer.",
        ],
    )

    # --- Engine ---
    box(
        d, 430, 110, 520, 280,
        "Agent execution engine",
        *GRAY,
        body=[
            "In-process FlowWorker (no Redis/Celery).",
            "Flow → Task → Subtask → Primary Agent → tool loop.",
            "After each subtask: refiner. After task: reporter.",
            "performPlanner wraps specialists with an adviser LLM plan — pkg/attackplan is not on this path.",
        ],
        footer=["1. createFlow / putUserInput"],
    )

    box(
        d, 430, 410, 520, 320,
        "Tools + Docker sandbox",
        *GRAY,
        body=[
            "terminal, file, browser, search engines, memory, graphiti_search,",
            "world_state_query, world_state_update, ask, done.",
            "Per-flow Kali container (docker.sock).",
            "After UpdateToolcallFinishedResult: IngestToolResult (best-effort).",
        ],
        footer=["3. Tool result  →  extract / ingest"],
    )

    # --- worldstate package ---
    rounded(d, (980, 110, 1620, 730), 12, YELLOW[0], YELLOW[1], 2)
    d.text((996, 122), "pkg/worldstate   (this is the core — not REST WorldStateService)", font=F_H, fill=INK)

    box(
        d, 996, 158, 608, 150,
        "Extract / Ingest",
        (255, 255, 255), YELLOW[1],
        body=[
            "ExtractCandidates: hosts, IPs, URLs, nmap/nxc services, CVEs, credentials.",
            "IngestToolResult → Store.Observe + Link. Active scans PromoteScanning.",
            "Agent tools: world_state_query (read) / world_state_update (Observe + Transition).",
        ],
    )
    box(
        d, 996, 322, 608, 200,
        "Store + lifecycle",
        (255, 255, 255), YELLOW[1],
        body=[
            "ValidateTransition() before any state UPDATE.",
            "unknown → discovered → scanning → assessed → vulnerable → exploited → remediated",
            "Skips: discovered→assessed, assessed|vulnerable→remediated. remediated is terminal.",
            "Observe / Link / Transition: entity + link + transition + journal event in 1 TX.",
        ],
        chips=["host", "service", "endpoint", "finding", "credential"],
    )
    box(
        d, 996, 536, 608, 176,
        "Delivery builder  (Primary only, next model turn)",
        (255, 255, 255), YELLOW[1],
        body=[
            "No cursor → baseline projection.  Cursor behind head, fits limits → delta.",
            "Over 64 events / 64 KiB / 128 entities|links → checkpoint.  Cursor == head → none.",
            "Persist <world_state> envelope + AdvanceWorldStateChainCursor in the same TX.",
            "FormatSnapshot also appended to execution context (not the dashboard JSON).",
        ],
    )

    # --- Postgres ---
    box(
        d, 1650, 110, 500, 420,
        "PostgreSQL  operational truth",
        *BLUE,
        body=["World State tables:"],
        chips=[
            "world_state_entities",
            "world_state_links",
            "world_state_transitions",
            "world_state_events",
            "world_state_chain_cursors",
            "agent_chain_waits",
        ],
        footer=[
            "world_state_events.revision: BIGINT under advisory lock 74192001.",
            "Not World State: msglogs, termlogs, toolcalls, tasks, subtasks.",
            "pgvector: memorist / embeddings — required, separate from WS.",
        ],
    )

    box(
        d, 1650, 550, 500, 180,
        "Optional  Graphiti / Neo4j",
        *GREEN,
        body=[
            "GRAPHITI_ENABLED. Episodes under group flow-{id}.",
            "Fed by agent/tool text — NOT synced from world_state_*.",
            "Used by graphiti_search and dashboard GET /worldstate label search.",
            "Not planner evidence. Not Primary delivery.",
        ],
    )

    box(
        d, 2170, 110, 510, 280,
        "REST WorldStateService  (UI only)",
        *WHITE,
        body=[
            "pkg/server/services/worldstate.go",
            "GET /worldstate — rebuilds a graph from tasks + stdin termlogs + optional Graphiti. riskLevel + availableActions live only here.",
            "GET /worldstate/lifecycle — reads real PG entities + transitions (flow-tab LifecycleStrip).",
            "GET /worldstate/toolcalls — toasts for world_state_* tools.",
            "Does not commit mutations. Does not allocate revisions. Does not inject envelopes.",
        ],
    )

    box(
        d, 2170, 410, 510, 320,
        "Frontend views  (three pictures)",
        *WHITE,
        body=[
            "Flow tab graph: client buildWorldState from GraphQL logs.",
            "Flow tab strip: GET /worldstate/lifecycle (real PG).",
            "Dashboard graph: GET /worldstate (reconstruction + Graphiti).",
            "Agents plan from the journal, not from these UI graphs.",
        ],
    )

    # numbered arrows top row
    h_arrow(d, 400, 430, 250, "1. input")
    h_arrow(d, 950, 980, 250, "2. tool result")
    h_arrow(d, 1620, 1650, 280, "3. commit 1 TX")

    # --- Bottom loop ---
    d.text((40, 770), "Operational feedback loop   (closed contour)", font=F_H, fill=INK)
    d.text(
        (40, 796),
        "Delivery is empty when the chain cursor already equals the captured event head. In-flight LLM calls are not interrupted.",
        font=F_S,
        fill=MUTED,
    )

    steps = [
        (40, "4. Mutation", "entity + link +\nlifecycle in 1 TX", YELLOW),
        (390, "5. Event Journal", "world_state_events\nordered fact", PURPLE),
        (740, "6. Revision N", "flow-scoped BIGINT\nadvisory lock", PURPLE),
        (1090, "7. Delivery", "baseline | delta |\ncheckpoint | none", PURPLE),
        (1440, "8. Primary Agent", "<world_state>\nnext model turn", ORANGE),
        (1790, "9. Next action", "tool, subtask,\nor ask wait", GRAY),
        (2140, "10. Execution", "tool run + result\n→ ingest", GRAY),
    ]
    y0 = 830
    for x, title, body, pal in steps:
        rounded(d, (x, y0, x + 320, y0 + 118), 10, pal[0], pal[1], 2)
        d.text((x + 14, y0 + 10), title, font=F_H, fill=INK)
        d.multiline_text((x + 14, y0 + 38), body, font=F_S, fill=MUTED, spacing=3)
    for i in range(len(steps) - 1):
        x1 = steps[i][0] + 320
        x2 = steps[i + 1][0]
        arrow(d, x1, y0 + 59, x2, y0 + 59)

    # return arrow
    arrow(d, 2300, y0 + 118, 2300, 1080)
    arrow(d, 2300, 1080, 200, 1080, "extract / ingest")
    arrow(d, 200, 1080, 200, y0 + 118)

    # wake
    box(
        d, 40, 1110, 1280, 140,
        "Wake path",
        *PINK,
        body=[
            "Primary ask registers agent_chain_waits. First winner: human input OR a new World State revision (also task_update / cancellation in schema).",
            "If World State already won, late human input is appended, not dropped. PrimaryWaitDispatcher leases waits (500 ms + post-commit hint), then Flow → Task → Subtask → ResumePrimaryWait.",
        ],
    )
    box(
        d, 1340, 1110, 1340, 140,
        "Not on the live loop  (exists in repo)",
        *WHITE,
        body=[
            "pkg/attackplan — revision-bound DAGs + PlannerEvidence search; performPlanner still calls adviser.",
            "Do not draw Attack Planner / Prompt Factory / Query Library / Milvus / Shodan / Censys as runtime boxes.",
        ],
    )

    # legend + concerns
    rounded(d, (40, 1280, 2680, 1820), 12, (255, 255, 255), (210, 210, 212), 1)
    d.text((56, 1296), "Separation of concerns", font=F_H, fill=INK)

    cols = [
        (56, "Execution history", "What agents/tools did.\nmsglogs, termlogs, toolcalls, tasks.\nNot World State."),
        (520, "Current World State", "What is known now.\nworld_state_entities + links\n+ lifecycle on each entity."),
        (980, "Event Journal", "What changed.\nworld_state_events, revision N.\nActor + facts + provenance."),
        (1440, "Delivery", "What this Primary chain has\nnot seen yet. Per-chain cursor.\nEnvelope + cursor in one TX."),
        (1900, "REST WorldStateService", "What the UI shows.\nOften a reconstruction.\nNever the agent write path."),
        (2360, "Auth / API", "Cookie sessions + OAuth +\nBearer API tokens.\nGraphQL live UI; REST extras."),
    ]
    for x, title, body in cols:
        d.text((x, 1334), title, font=F_H, fill=BLUE[1])
        d.multiline_text((x, 1362), body, font=F_S, fill=MUTED, spacing=3)

    d.text((56, 1470), "Removed from the previous drawing", font=F_H, fill=PINK[1])
    removed = (
        "WorldStateService as mutation orchestrator  ·  risk + available actions inside the core  ·  "
        "lifecycle names mapped / exercised  ·  Neo4j sync from entities/links  ·  "
        "tables world_state_snapshots, world_state_agents, agent_checkpoints, world_state_mutations  ·  "
        "Milvus / Elasticsearch / Shodan / Censys / Prompt Factory / Query Library  ·  "
        "Attack Planner on the main loop  ·  files/tasks/artifacts as World State storage  ·  "
        "fictional agents (PentestBot, EmailOps, RedTeam, Inspector, Watcher)."
    )
    y = 1500
    for line in wrap(d, removed, F_S, 2580):
        d.text((56, y), line, font=F_S, fill=MUTED)
        y += 18

    d.text((56, 1588), "Live agent map", font=F_H, fill=INK)
    agent_map = [
        ("primary_agent", "Subtask orchestrator; only recipient of <world_state> delivery"),
        ("generator / refiner / reporter", "Task lifecycle: plan subtasks, replan, write result"),
        ("pentester / coder / installer", "Sandbox specialists (terminal/file/browser)"),
        ("searcher / enricher / memorist", "Web search + pgvector memory"),
        ("adviser", "performPlanner / advice; not pkg/attackplan"),
        ("reflector / summarizer / tool_call_fixer", "Chain hygiene"),
        ("assistant", "Parallel per-flow chat; no auto World State delivery"),
        ("web_reporter", "Structured web-pentest report chain"),
    ]
    col_w = 640
    for i, (name, desc) in enumerate(agent_map):
        col, row = i % 4, i // 4
        x, y = 56 + col * col_w, 1624 + row * 44
        d.text((x, y), name, font=F_CHIP, fill=ORANGE[1])
        d.text((x, y + 16), desc, font=F_XS, fill=MUTED)

    d.text(
        (56, 1788),
        "Source: backend/pkg/worldstate, providers/world_state_delivery.go, providers/performers.go, server/services/worldstate.go, GraphQL AgentType / MsgchainType.  Sep 2026.",
        font=F_XS,
        fill=(140, 140, 145),
    )

    img.save(OUT, "PNG", dpi=(144, 144))
    print(OUT, img.size)


if __name__ == "__main__":
    main()
