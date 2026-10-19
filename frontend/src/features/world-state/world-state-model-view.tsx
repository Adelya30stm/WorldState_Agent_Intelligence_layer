import { RefreshCw } from 'lucide-react';
import { useMemo, useState } from 'react';

import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

import type { WorldStateModel, WorldStateRun } from './world-state-model-types';

const STATUS_LABELS: Record<string, string> = {
    unknown: 'Not checked',
    in_progress: 'In progress',
    vulnerable: 'Vulnerable',
    not_confirmed: 'Not confirmed',
    inconclusive: 'Inconclusive',
    planned: 'Planned',
    running: 'Running',
    completed: 'Completed',
    failed: 'Failed',
    cancelled: 'Cancelled',
    not_reproduced: 'Not reproduced',
};

function Status({ value }: { value: string }) {
    const color =
        value === 'vulnerable'
            ? 'bg-red-500/15 text-red-600 dark:text-red-300'
            : value === 'failed' || value === 'inconclusive'
              ? 'bg-amber-500/15 text-amber-700 dark:text-amber-300'
              : value === 'running' || value === 'in_progress'
                ? 'bg-blue-500/15 text-blue-600 dark:text-blue-300'
                : 'bg-muted text-muted-foreground';
    return <span className={`rounded px-2 py-0.5 text-xs ${color}`}>{STATUS_LABELS[value] ?? value}</span>;
}

function JSONDetails({ label, value }: { label: string; value: Record<string, unknown> }) {
    return (
        <details className="bg-muted/20 min-w-0 rounded border p-2">
            <summary className="cursor-pointer text-xs font-medium">{label}</summary>
            <pre className="mt-2 max-h-80 overflow-auto text-xs break-all whitespace-pre-wrap">
                {JSON.stringify(value, null, 2)}
            </pre>
        </details>
    );
}

function Run({ run }: { run: WorldStateRun }) {
    return (
        <article
            className="space-y-2 rounded border p-3"
            aria-label={`Run ${run.id}`}
        >
            <div className="flex flex-wrap items-center gap-2 text-xs">
                <strong>Run #{run.id}</strong>
                <Status value={run.execution_status} />
                {run.verdict && <Status value={run.verdict} />}
                <time
                    className="text-muted-foreground"
                    dateTime={run.created_at}
                >
                    {new Date(run.created_at).toLocaleString()}
                </time>
                {run.tool_call_id !== null && (
                    <span className="text-muted-foreground">Tool call #{run.tool_call_id}</span>
                )}
            </div>
            <div className="grid gap-2 md:grid-cols-2">
                <JSONDetails
                    label="Parameters"
                    value={run.parameters}
                />
                <JSONDetails
                    label="Evidence"
                    value={run.evidence}
                />
            </div>
            {(run.started_at || run.finished_at) && (
                <p className="text-muted-foreground text-xs">
                    {run.started_at && `Started: ${new Date(run.started_at).toLocaleString()}`}
                    {run.finished_at && ` · Finished: ${new Date(run.finished_at).toLocaleString()}`}
                </p>
            )}
        </article>
    );
}

interface Props {
    model: WorldStateModel;
    loading?: boolean;
    error?: string | null;
    onRefresh?: () => void;
    onBack?: () => void;
    findingsOnly?: boolean;
}

/** Values from the API are rendered as escaped text, including untrusted payloads/evidence. */
export function WorldStateModelView({ model, loading, error, onRefresh, onBack, findingsOnly = false }: Props) {
    const [search, setSearch] = useState('');
    const objects = useMemo(() => {
        const needle = search.trim().toLowerCase();
        return model.objects
            .map((object) => ({
                ...object,
                checks: findingsOnly
                    ? object.checks.filter((check) => check.computed_status === 'vulnerable')
                    : object.checks,
            }))
            .filter(
                (object) =>
                    (!findingsOnly || object.checks.length > 0) &&
                    (!needle ||
                        `${object.object_key} ${object.type} ${JSON.stringify(object.locator)} ${object.checks.map((check) => check.kind).join(' ')}`
                            .toLowerCase()
                            .includes(needle)),
            );
    }, [model, findingsOnly, search]);
    const counts = findingsOnly
        ? {
              objects: objects.length,
              checks: objects.reduce((total, object) => total + object.checks.length, 0),
              runs: objects.reduce(
                  (total, object) => total + object.checks.reduce((count, check) => count + check.run_count, 0),
                  0,
              ),
          }
        : { objects: model.object_count, checks: model.check_count, runs: model.run_count };

    return (
        <section
            className="bg-background text-foreground flex h-full min-h-0 flex-col"
            aria-label="World State checks"
        >
            <div className="flex shrink-0 flex-wrap items-center gap-2 border-b px-4 py-3">
                <h2 className="text-sm font-semibold">{findingsOnly ? 'Confirmed findings' : 'Objects and checks'}</h2>
                <span className="text-muted-foreground text-xs">
                    {counts.objects} objects · {counts.checks} checks · {counts.runs} runs
                </span>
                <div className="ml-auto flex gap-2">
                    {onRefresh && (
                        <Button
                            aria-label="Refresh World State"
                            className="h-7"
                            onClick={onRefresh}
                            variant="outline"
                        >
                            <RefreshCw className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
                        </Button>
                    )}
                    {onBack && (
                        <Button
                            className="h-7 text-xs"
                            onClick={onBack}
                            variant="outline"
                        >
                            ← Back
                        </Button>
                    )}
                </div>
            </div>
            {error && (
                <p
                    className="text-destructive px-4 py-2 text-xs"
                    role="alert"
                >
                    {error} Showing the last loaded data.
                </p>
            )}
            <div className="shrink-0 px-4 py-3">
                <Input
                    aria-label="Search objects and checks"
                    className="h-8 text-xs"
                    placeholder="Search object, address or check kind"
                    onChange={(event) => setSearch(event.target.value)}
                    value={search}
                />
            </div>
            <div className="min-h-0 flex-1 space-y-3 overflow-auto px-4 pb-4">
                {objects.length === 0 && (
                    <p className="text-muted-foreground py-8 text-center text-sm">
                        {search
                            ? 'No matching objects or checks.'
                            : findingsOnly
                              ? 'No confirmed vulnerabilities recorded.'
                              : 'No objects recorded yet. Objects and check results will appear as the agent records them.'}
                    </p>
                )}
                {objects.map((object) => (
                    <details
                        className="rounded-lg border"
                        key={object.id}
                        open
                    >
                        <summary className="cursor-pointer px-3 py-3 text-sm break-all">
                            <span className="mr-2 font-mono">{object.object_key}</span>
                            <Status value={object.computed_status} />
                            <span className="text-muted-foreground ml-2 text-xs">
                                {object.type} · {object.check_count} checks · {object.run_count} runs
                            </span>
                        </summary>
                        <div className="space-y-3 border-t p-3">
                            <JSONDetails
                                label="Object location"
                                value={object.locator}
                            />
                            {object.checks.length === 0 && (
                                <p className="text-muted-foreground text-xs">No checks recorded for this object.</p>
                            )}
                            {object.checks.map((check) => (
                                <details
                                    className="rounded border"
                                    key={check.id}
                                >
                                    <summary className="cursor-pointer px-3 py-2 text-sm">
                                        <strong className="mr-2">{check.kind}</strong>
                                        <Status value={check.computed_status} />
                                        <span className="text-muted-foreground ml-2 text-xs">
                                            Check #{check.id} · {check.run_count} runs
                                        </span>
                                    </summary>
                                    <div className="space-y-2 border-t p-3">
                                        {check.runs.length === 0 && (
                                            <p className="text-muted-foreground text-xs">This check has no runs yet.</p>
                                        )}
                                        {check.runs.map((run) => (
                                            <Run
                                                key={run.id}
                                                run={run}
                                            />
                                        ))}
                                    </div>
                                </details>
                            ))}
                        </div>
                    </details>
                ))}
            </div>
        </section>
    );
}
