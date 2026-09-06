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
        const filters = [str(args.type), list(args.types), str(args.state), list(args.states)]
            .filter(Boolean)
            .join(' · ');
        let found = '';

        try {
            const parsed = JSON.parse(call.result) as { count?: number };

            if (typeof parsed.count === 'number') {found = ` → ${parsed.count} entities`;}
        } catch {
            /* result not JSON — ignore */
        }

        const description = [message, filters ? `filter: ${filters}` : '', found]
            .filter(Boolean)
            .join('\n');

        return { description: description || 'Agent read World State', title: '🌐 world_state_query' };
    }

    // world_state_update
    const batch = Array.isArray(args.entities) ? args.entities.length : 0;
    const key = str(args.entity_key);
    const toState = str(args.to_state);
    const transition = batch > 0 ? `batch ${batch}` : [key, toState].filter(Boolean).join(' → ');
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

function list(v: unknown): string {
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string' && x.length > 0).join(',') : '';
}
