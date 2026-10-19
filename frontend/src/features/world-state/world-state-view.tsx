import { Globe, Layers, RefreshCw, Workflow, Zap } from 'lucide-react';
import { useCallback, useState } from 'react';

import { Button } from '@/components/ui/button';
import { useFlow } from '@/providers/flow-provider';

import type { GraphMode, WorldState, WorldStateEntity } from './world-state-types';
import { GRAPH_MODE_LABELS, GRAPH_MODES } from './world-state-types';
import { useWorldState } from './world-state-hooks';
import WorldStateDetailsPanel from './world-state-details-panel';
import WorldStateGraph from './world-state-graph';
import WorldStateSidebar from './world-state-sidebar';
import { LifecycleStrip } from './lifecycle-strip';
import { useWorldStateModel } from './use-world-state-model';
import { WorldStateModelView } from './world-state-model-view';

// ─── Mode icons ───────────────────────────────────────────────────────────────

const MODE_ICONS: Record<GraphMode, React.ReactNode> = {
    execution: <Workflow className="size-3.5" />,
    target: <Globe className="size-3.5" />,
    'threat-model': <Zap className="size-3.5" />,
};

// ─── Empty state ──────────────────────────────────────────────────────────────

const EmptyState = ({ onBack }: { onBack?: () => void }) => (
    <div className="flex h-full flex-col">
        {onBack && (
            <div className="flex items-center gap-2 px-4 py-2 border-b shrink-0">
                <Button className="h-7 text-xs" onClick={onBack} variant="outline">
                    ← Back
                </Button>
            </div>
        )}
        <div className="flex flex-1 flex-col items-center justify-center gap-4 text-muted-foreground">
            <div className="size-16 rounded-full bg-muted flex items-center justify-center">
                <Layers className="size-8 opacity-40" />
            </div>
            <div className="text-center space-y-1">
                <p className="text-sm font-medium">No entities extracted yet</p>
                <p className="text-xs opacity-60 max-w-xs">
                    Run some commands or start automation — World State will extract targets,
                    tools, and findings automatically.
                </p>
            </div>
        </div>
    </div>
);

// ─── Stats bar ────────────────────────────────────────────────────────────────

const StatsBar = ({ worldState }: { worldState: WorldState }) => {
    const byType = worldState.entities.reduce<Record<string, number>>((acc, e) => {
        acc[e.type] = (acc[e.type] ?? 0) + 1;
        return acc;
    }, {});

    const stats = [
        { key: 'domain', label: 'Domains' },
        { key: 'endpoint', label: 'Endpoints' },
        { key: 'tool', label: 'Tools' },
        { key: 'finding', label: 'Findings' },
        { key: 'task', label: 'Tasks' },
    ].filter((s) => (byType[s.key] ?? 0) > 0);

    return (
        <div className="flex items-center gap-3 px-4 py-1.5 border-b bg-muted/30 text-xs text-muted-foreground overflow-x-auto shrink-0">
            <span className="font-semibold text-foreground shrink-0">
                {worldState.entities.length} entities · {worldState.links.length} links
            </span>
            {stats.map((s) => (
                <span className="shrink-0" key={s.key}>
                    {byType[s.key]} {s.label}
                </span>
            ))}
            <span className="ml-auto shrink-0 opacity-50">
                Updated {worldState.updatedAt.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
            </span>
        </div>
    );
};

// ─── Main content (only rendered when worldState exists) ──────────────────────

interface ContentProps {
    worldState: WorldState;
    onBack?: () => void;
}

const WorldStateContent = ({ worldState: initialWorldState, onBack }: ContentProps) => {
    const [mode, setMode] = useState<GraphMode>('execution');
    const [selectedId, setSelectedId] = useState<string | null>(null);
    // localState allows in-memory annotation changes (notes, priority) without re-fetching
    const [localState, setLocalState] = useState<WorldState | null>(null);

    const worldState = localState ?? initialWorldState;
    const selectedEntity = worldState.entities.find((e) => e.id === selectedId) ?? null;

    const handleEntityUpdate = useCallback(
        (updated: WorldStateEntity) => {
            setLocalState((prev) => {
                const base = prev ?? initialWorldState;
                return {
                    ...base,
                    entities: base.entities.map((e) => (e.id === updated.id ? updated : e)),
                };
            });
        },
        [initialWorldState],
    );

    return (
        <div className="flex h-full flex-col bg-background">
            {/* Toolbar */}
            <div className="flex items-center gap-2 px-4 py-2 border-b bg-background shrink-0">
                <div className="flex gap-1 bg-muted rounded-lg p-0.5">
                    {GRAPH_MODES.map((m) => (
                        <button
                            className={`flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs font-medium transition-colors ${
                                mode === m
                                    ? 'bg-background text-foreground shadow-sm'
                                    : 'text-muted-foreground hover:text-foreground'
                            }`}
                            key={m}
                            onClick={() => setMode(m)}
                        >
                            {MODE_ICONS[m]}
                            {GRAPH_MODE_LABELS[m]}
                        </button>
                    ))}
                </div>

                <div className="ml-auto flex items-center gap-2">
                    {localState && (
                        <Button
                            className="h-7 gap-1.5 text-xs"
                            onClick={() => { setLocalState(null); setSelectedId(null); }}
                            variant="ghost"
                        >
                            <RefreshCw className="size-3" />
                            Reset
                        </Button>
                    )}
                    {onBack && (
                        <Button className="h-7 text-xs" onClick={onBack} variant="outline">
                            ← Back
                        </Button>
                    )}
                </div>
            </div>

            {/* Stats */}
            <StatsBar worldState={worldState} />

            {/* Body */}
            <div className="flex flex-1 overflow-hidden">
                {/* Left sidebar */}
                <div className="w-52 shrink-0 overflow-hidden">
                    <WorldStateSidebar
                        onSelect={setSelectedId}
                        selectedId={selectedId}
                        worldState={worldState}
                    />
                </div>

                {/* Graph */}
                <div className="flex-1 overflow-hidden">
                    <WorldStateGraph
                        mode={mode}
                        onEntitySelect={setSelectedId}
                        selectedEntityId={selectedId}
                        worldState={worldState}
                    />
                </div>

                {/* Details panel — animates in/out */}
                <div
                    className="shrink-0 overflow-hidden transition-all duration-300 ease-in-out"
                    style={{ width: selectedId ? 280 : 0 }}
                >
                    <div style={{ width: 280, height: '100%' }}>
                        <WorldStateDetailsPanel
                            entity={selectedEntity}
                            onClose={() => setSelectedId(null)}
                            onEntityUpdate={handleEntityUpdate}
                            worldState={worldState}
                        />
                    </div>
                </div>
            </div>

            <LifecycleStrip flowId={String(worldState.flowId)} />
        </div>
    );
};

// ─── Public component ─────────────────────────────────────────────────────────

interface WorldStateViewProps {
    onBack?: () => void;
}

const LegacyWorldStateView = ({ onBack }: WorldStateViewProps) => {
    const worldState = useWorldState();
    const { flowId } = useFlow();

    if (!worldState || worldState.entities.length === 0) {
        return (
            <div className="flex h-full flex-col">
                <div className="flex-1 min-h-0">
                    <EmptyState onBack={onBack} />
                </div>
                {flowId && <LifecycleStrip flowId={flowId} />}
            </div>
        );
    }

    return (
        <WorldStateContent
            onBack={onBack}
            worldState={worldState}
        />
    );
};

const WorldStateView = ({ onBack }: WorldStateViewProps) => {
    const { flowId } = useFlow();
    const { data, error, loading, refresh } = useWorldStateModel(flowId);
    if (!data) {
        return <div className="flex h-full flex-col items-center justify-center gap-3 p-4 text-sm text-muted-foreground">
            <p role={error ? 'alert' : 'status'}>{error ?? (flowId ? 'Loading World State…' : 'Select a flow to see World State.')}</p>
            {error && <Button onClick={refresh} variant="outline">Retry</Button>}
            {onBack && <Button onClick={onBack} variant="outline">← Back</Button>}
        </div>;
    }
    if (data.schema_version === 1) return <LegacyWorldStateView key={flowId} onBack={onBack} />;
    return <WorldStateModelView key={flowId} model={data} error={error} loading={loading} onRefresh={refresh} onBack={onBack} />;
};

export default WorldStateView;
