import { useEffect, useRef } from 'react';
import { toast } from 'sonner';

import { axios } from '@/lib/axios';

interface ToolCallsData {
    flowId: number;
    toolCalls: WorldStateToolCall[];
}

interface WorldStateToolCall {
    args: Record<string, unknown>;
    createdAt: string;
    id: number;
    name: string;
    result: string;
    status: string;
}

const POLL_INTERVAL_MS = 4000;

/**
 * Polls the World State tool-call feed for a flow and pops a toast each time an
 * agent invokes world_state_query / world_state_update. Existing calls present
 * on first load are seeded silently so only calls that fire *after* mount pop.
 */
export function useWorldStateToolCallPopup(flowId: null | string): void {
    const seenRef = useRef<Set<number>>(new Set());
    const seededRef = useRef(false);

    useEffect(() => {
        if (!flowId) {return;}

        // Reset per-flow so switching flows re-seeds cleanly.
        seenRef.current = new Set();
        seededRef.current = false;
        let cancelled = false;

        const poll = async () => {
            try {
                const body = (await axios.get(`/flows/${flowId}/worldstate/toolcalls`)) as Partial<ToolCallsData> & {
                    data?: ToolCallsData;
                };
                const calls = (body.data ?? (body as ToolCallsData)).toolCalls ?? [];

                if (cancelled) {return;}

                if (!seededRef.current) {
                    // First successful load — remember everything, pop nothing.
                    calls.forEach((c) => seenRef.current.add(c.id));
                    seededRef.current = true;

                    return;
                }

                // Endpoint returns newest-first; pop oldest-first so order reads naturally.
                for (const call of [...calls].reverse()) {
                    if (seenRef.current.has(call.id)) {continue;}

                    seenRef.current.add(call.id);
                    notify(call);
                }
            } catch {
                /* transient fetch error — next tick retries */
            }
        };

        poll();
        const id = setInterval(poll, POLL_INTERVAL_MS);

        return () => {
            cancelled = true;
            clearInterval(id);
        };
    }, [flowId]);
}

/** Human-readable one-liner describing what the agent did in this call. */
function describe(call: WorldStateToolCall): { description: string; title: string; } {
    const args = call.args ?? {};
    const message = str(args.message);

    if (call.name === 'world_state_query') {
        const filters = [str(args.type), str(args.state), str(args.kind), args.object_id ? `object #${args.object_id}` : '', args.check_id ? `check #${args.check_id}` : ''].filter(Boolean).join(' · ');
        let found = '';

        try {
            const parsed = JSON.parse(call.result) as { count?: number; schema_version?: number; objects?: unknown[] };

            if (parsed.schema_version === 2 && Array.isArray(parsed.objects)) {found = ` → ${parsed.objects.length} objects`;}
            else if (typeof parsed.count === 'number') {found = ` → ${parsed.count} entities`;}
        } catch {
            /* result not JSON — ignore */
        }

        const description = [message, filters ? `filter: ${filters}` : '', found]
            .filter(Boolean)
            .join('\n');

        return { description: description || 'Agent read World State', title: '🌐 world_state_query' };
    }

    // world_state_update
    const operation = str(args.operation);
    if (operation) {
        const operations: Record<string, string> = {
            observe_object: 'Recorded an object', ensure_check: 'Registered a check',
            start_run: 'Started a check run', finish_run: 'Recorded a run result',
        };
        const target = str(args.object_key) || (args.run_id ? `run #${args.run_id}` : args.check_id ? `check #${args.check_id}` : args.object_id ? `object #${args.object_id}` : '');
        const description = [message, operations[operation] ?? operation, target, str(args.kind), str(args.execution_status), str(args.verdict)].filter(Boolean).join(' · ');
        return { description, title: '✏️ world_state_update' };
    }
    const key = str(args.entity_key);
    const toState = str(args.to_state);
    const transition = [key, toState].filter(Boolean).join(' → ');
    const description = [message, transition].filter(Boolean).join('\n');

    return { description: description || 'Agent wrote World State', title: '✏️ world_state_update' };
}

function notify(call: WorldStateToolCall): void {
    const { description, title } = describe(call);
    const failed = call.status === 'failed' || call.status === 'error';
    const opts = { description, duration: 6000 };

    if (failed) {
        toast.error(title, opts);
    } else if (call.name === 'world_state_update') {
        toast.success(title, opts);
    } else {
        toast.info(title, opts);
    }
}

function str(v: unknown): string {
    return typeof v === 'string' ? v : '';
}
