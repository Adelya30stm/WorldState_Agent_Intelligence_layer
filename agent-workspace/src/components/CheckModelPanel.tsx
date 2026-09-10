import { RefreshCw } from 'lucide-react';
import { WorldStateModel } from '@/lib/api';

function Status({ value }: { value: string }) {
    const color = value === 'vulnerable' ? 'bg-red-900/60 text-red-200' : value === 'running' || value === 'in_progress' ? 'bg-blue-900/60 text-blue-200' : 'bg-slate-800 text-slate-300';
    return <span className={`rounded px-1.5 py-0.5 text-[10px] ${color}`}>{value.replace(/_/g, ' ')}</span>;
}

function JSONDetails({ title, value }: { title: string; value: Record<string, unknown> }) {
    return <details className="min-w-0 rounded border border-slate-800 p-2">
        <summary className="cursor-pointer text-xs text-slate-400">{title}</summary>
        <pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-all text-xs text-slate-300">{JSON.stringify(value, null, 2)}</pre>
    </details>;
}

export function CheckModelPanel({ model, loading, error, onRefresh }: {
    model: WorldStateModel | null;
    loading: boolean;
    error: string | null;
    onRefresh: () => void;
}) {
    return <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-xl border border-indigo-900/40 bg-[#0d1117] text-slate-200">
        <div className="flex shrink-0 items-center gap-2 border-b border-indigo-900/30 px-4 py-3">
            <h2 className="text-sm font-semibold">Objects and checks</h2>
            {model && <span className="text-xs text-slate-500">{model.object_count} objects · {model.check_count} checks · {model.run_count} runs</span>}
            <button className="ml-auto rounded p-1.5 hover:bg-white/5" aria-label="Refresh World State" onClick={onRefresh}><RefreshCw size={14} className={loading ? 'animate-spin' : ''} /></button>
        </div>
        {error && <p className="px-4 py-2 text-xs text-red-400" role="alert">{error}</p>}
        <div className="min-h-0 flex-1 space-y-3 overflow-auto p-3">
            {!model && <p className="py-8 text-center text-sm text-slate-500">{loading ? 'Loading World State…' : 'Select a flow to see World State.'}</p>}
            {model?.objects.length === 0 && <p className="py-8 text-center text-sm text-slate-500">No objects recorded yet.</p>}
            {model?.objects.map((object) => <details className="rounded-lg border border-indigo-900/30" key={object.id} open>
                <summary className="cursor-pointer break-all p-3 text-xs"><span className="mr-2 font-mono">{object.object_key}</span><Status value={object.computed_status} /><span className="ml-2 text-slate-500">{object.type} · {object.check_count} checks</span></summary>
                <div className="space-y-3 border-t border-indigo-900/30 p-3">
                    <JSONDetails title="Object location" value={object.locator} />
                    {object.checks.length === 0 && <p className="text-xs text-slate-500">No checks recorded.</p>}
                    {object.checks.map((check) => <details className="rounded border border-slate-800" key={check.id}>
                        <summary className="cursor-pointer p-2 text-xs"><strong className="mr-2">{check.kind}</strong><Status value={check.computed_status} /><span className="ml-2 text-slate-500">Check #{check.id} · {check.run_count} runs</span></summary>
                        <div className="space-y-3 border-t border-slate-800 p-2">
                            {check.runs.length === 0 && <p className="text-xs text-slate-500">No runs yet.</p>}
                            {check.runs.map((run) => <article key={run.id} className="space-y-2 rounded border border-slate-800 p-2">
                                <div className="flex flex-wrap items-center gap-2 text-xs"><strong>Run #{run.id}</strong><Status value={run.execution_status} />{run.verdict && <Status value={run.verdict} />}<time className="text-slate-500" dateTime={run.created_at}>{new Date(run.created_at).toLocaleString()}</time>{run.tool_call_id !== null && <span className="text-slate-500">Tool call #{run.tool_call_id}</span>}</div>
                                <div className="grid gap-2 lg:grid-cols-2"><JSONDetails title="Parameters" value={run.parameters} /><JSONDetails title="Evidence" value={run.evidence} /></div>
                            </article>)}
                        </div>
                    </details>)}
                </div>
            </details>)}
        </div>
    </section>;
}
