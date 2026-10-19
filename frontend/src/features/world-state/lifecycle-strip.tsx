import { useCallback, useEffect, useState } from 'react';
import { ArrowRight, RefreshCw } from 'lucide-react';

import { Button } from '@/components/ui/button';
import { axios } from '@/lib/axios';

interface LifecycleEntity {
    id: number;
    entityKey: string;
    type: string;
    state: string;
    updatedAt: string;
}

interface LifecycleTransition {
    id: number;
    entityKey: string;
    fromState: string;
    toState: string;
    agent: string;
    createdAt: string;
}

interface LifecycleData {
    flowId: number;
    entities: LifecycleEntity[];
    transitions: LifecycleTransition[];
    counts: Record<string, number>;
    snapshot: string;
}

interface ScopeCandidate {
    id: number;
    entityKey: string;
    type: string;
    reason: string;
}

interface CandidatesData {
    flowId: number;
    scope: { domains: string[]; cidrs: string[] };
    candidates: ScopeCandidate[];
}

const STATE_CLS: Record<string, string> = {
    unknown: 'bg-muted text-muted-foreground',
    discovered: 'bg-sky-500/15 text-sky-600 dark:text-sky-300',
    scanning: 'bg-amber-500/15 text-amber-700 dark:text-amber-300',
    assessed: 'bg-indigo-500/15 text-indigo-600 dark:text-indigo-300',
    vulnerable: 'bg-orange-500/15 text-orange-700 dark:text-orange-300',
    exploited: 'bg-red-500/15 text-red-600 dark:text-red-300',
    remediated: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300',
};

function Pill({ state }: { state: string }) {
    return (
        <span className={`rounded px-1.5 py-0.5 text-[10px] font-mono ${STATE_CLS[state] ?? 'bg-muted'}`}>
            {state}
        </span>
    );
}

function unwrap<T>(body: { data?: T } & T): T {
    return body.data ?? body;
}

/** Persisted PG world_state_* view used by the planner. */
export function LifecycleStrip({ flowId }: { flowId: string }) {
    const [data, setData] = useState<LifecycleData | null>(null);
    const [candidates, setCandidates] = useState<CandidatesData | null>(null);
    const [error, setError] = useState<string | null>(null);
    const [reviewing, setReviewing] = useState(false);

    const load = useCallback(async () => {
        try {
            const [lifeBody, candBody] = await Promise.all([
                axios.get(`/flows/${flowId}/worldstate/lifecycle`) as Promise<{ data?: LifecycleData } & LifecycleData>,
                axios.get(`/flows/${flowId}/worldstate/candidates`) as Promise<
                    { data?: CandidatesData } & CandidatesData
                >,
            ]);
            setData(unwrap(lifeBody));
            setCandidates(unwrap(candBody));
            setError(null);
        } catch (e) {
            setError(e instanceof Error ? e.message : 'failed to load lifecycle');
        }
    }, [flowId]);

    useEffect(() => {
        load();
        const id = setInterval(load, 12000);
        return () => clearInterval(id);
    }, [load]);

    const review = async (action: 'accept' | 'reject') => {
        const keys = (candidates?.candidates ?? []).map((c) => c.entityKey);
        if (!keys.length || reviewing) {
            return;
        }
        setReviewing(true);
        try {
            await axios.post(`/flows/${flowId}/worldstate/candidates/review`, {
                accept: action === 'accept' ? keys : [],
                reject: action === 'reject' ? keys : [],
            });
            await load();
        } catch (e) {
            setError(e instanceof Error ? e.message : 'failed to review candidates');
        } finally {
            setReviewing(false);
        }
    };

    const entities = data?.entities ?? [];
    const transitions = data?.transitions?.slice(0, 12) ?? [];
    const counts = data?.counts ?? {};
    const pending = candidates?.candidates ?? [];
    const scopeDomains = candidates?.scope?.domains ?? [];

    return (
        <div className="border-t bg-muted/20 shrink-0">
            <div className="flex items-center gap-2 px-4 py-1.5 border-b text-xs">
                <span className="font-semibold">Planner Lifecycle</span>
                <span className="text-muted-foreground">{entities.length} assets in Postgres</span>
                {scopeDomains.length > 0 && (
                    <span className="truncate text-muted-foreground">scope: {scopeDomains.join(', ')}</span>
                )}
                {Object.entries(counts).map(([s, n]) => (
                    <span key={s} className="flex items-center gap-1">
                        <Pill state={s} />
                        <span className="text-muted-foreground">{n}</span>
                    </span>
                ))}
                <Button className="ml-auto h-6 w-6 p-0" onClick={load} size="icon" variant="ghost">
                    <RefreshCw className="size-3" />
                </Button>
            </div>
            {pending.length > 0 && (
                <div className="border-b bg-amber-500/10 px-4 py-2 text-xs">
                    <div className="flex items-center gap-2">
                        <span className="font-semibold">{pending.length} similar hosts need review</span>
                        <Button
                            className="ml-auto h-6 px-2"
                            disabled={reviewing}
                            onClick={() => review('accept')}
                            size="sm"
                            variant="default"
                        >
                            Accept all
                        </Button>
                        <Button
                            className="h-6 px-2"
                            disabled={reviewing}
                            onClick={() => review('reject')}
                            size="sm"
                            variant="outline"
                        >
                            Reject all
                        </Button>
                    </div>
                    <div className="mt-1 max-h-20 overflow-y-auto font-mono text-[11px] text-muted-foreground">
                        {pending.map((c) => (
                            <div key={c.id}>{c.entityKey}</div>
                        ))}
                    </div>
                </div>
            )}
            {error && <p className="px-4 py-1 text-xs text-destructive">{error}</p>}
            <div className="grid max-h-40 grid-cols-2 overflow-hidden">
                <div className="overflow-y-auto border-r">
                    {entities.slice(0, 20).map((e) => (
                        <div className="flex items-center gap-2 border-b px-3 py-1 text-xs" key={e.id}>
                            <Pill state={e.state} />
                            <span className="truncate font-mono text-muted-foreground">{e.entityKey}</span>
                        </div>
                    ))}
                    {entities.length === 0 && (
                        <p className="px-3 py-3 text-xs text-muted-foreground">No persisted entities yet</p>
                    )}
                </div>
                <div className="overflow-y-auto">
                    {transitions.map((t) => (
                        <div className="border-b px-3 py-1 text-xs" key={t.id}>
                            <div className="truncate font-mono text-[10px] text-muted-foreground">{t.entityKey}</div>
                            <div className="mt-0.5 flex items-center gap-1">
                                <Pill state={t.fromState} />
                                <ArrowRight className="size-3 opacity-40" />
                                <Pill state={t.toState} />
                                <span className="ml-1 text-[10px] text-muted-foreground">{t.agent}</span>
                            </div>
                        </div>
                    ))}
                </div>
            </div>
        </div>
    );
}
