import { useState } from 'react';
import { ApolloProvider } from '@apollo/client';
import { client } from '@/lib/apollo';
import { FlowSelector } from '@/components/FlowSelector';
import { NewFlowModal } from '@/components/NewFlowModal';
import { WorldStateGraph } from '@/components/WorldStateGraph';
import { AgentPanel } from '@/components/AgentPanel';
import { DirectiveFeed } from '@/components/DirectiveFeed';
import { NextStepPanel } from '@/components/NextStepPanel';
import { AgentStateTimeline } from '@/components/AgentStateTimeline';
import { StateMachineMap } from '@/components/StateMachineMap';
import { LifecyclePanel } from '@/components/LifecyclePanel';
import { CheckModelPanel } from '@/components/CheckModelPanel';
import { useWorldState } from '@/hooks/useWorldState';
import { useAgentTransitions, AgentStatus } from '@/hooks/useAgentTransitions';
import { ExternalLink, GitBranch, Map } from 'lucide-react';

type Tab = 'graph' | 'lifecycle' | 'transitions';

// Rabbit logo — sitting side profile
function RabbitIcon({ size = 28, color = 'white' }: { size?: number; color?: string }) {
    const eye = color === 'white' ? '#4c1d95' : '#0a0d14';
    return (
        <svg width={size} height={size} viewBox="0 0 64 64" fill="none" xmlns="http://www.w3.org/2000/svg">
            <g fill={color}>
                <ellipse cx="26" cy="45" rx="17" ry="14" />
                <ellipse cx="43" cy="48" rx="9" ry="13" />
                <ellipse cx="31" cy="60" rx="20" ry="4" />
                <circle cx="45" cy="31" r="10.5" />
                <path d="M50 28 C57 29 57 38 50 39 C47 39 46 30 50 28 Z" />
                <path d="M38 24 C33 16 32 6 36 3 C39 1 42 3 43 8 C44 14 44 20 43 25 Z" />
                <path d="M43 24 C41 15 42 6 46 4 C49 3 51 6 51 11 C51 17 49 22 47 25 Z" />
            </g>
            <circle cx="47" cy="29" r="1.6" fill={eye} />
        </svg>
    );
}

function WSLogo() {
    return (
        <div className="flex items-center gap-3">
            <div className="relative w-10 h-10 bg-gradient-to-br from-violet-800 via-indigo-800 to-violet-950 rounded-xl shadow-lg shadow-violet-900/60 flex items-center justify-center border border-violet-500/25">
                <RabbitIcon size={36} color="white" />
            </div>
            <div>
                <p className="text-[13px] font-bold text-white tracking-tight leading-none">WorldState<span className="text-violet-400">Security</span></p>
                <p className="text-[9px] text-violet-400/70 font-medium uppercase tracking-[0.12em] leading-none mt-1">
                    Agent State Monitor
                </p>
            </div>
        </div>
    );
}

function WorkspaceContent() {
    const [flowId, setFlowId] = useState<string | null>(null);
    const [tab, setTab] = useState<Tab>('graph');
    const { data: wsData, model: wsModel, loading: wsLoading, error: wsError, refresh: wsRefresh } = useWorldState(flowId);
    const { transitions, clear } = useAgentTransitions(flowId);

    const entities = wsData?.entities ?? [];
    const links = wsData?.links ?? [];

    // Derive current state per agent from latest transitions
    const currentStates: Record<string, AgentStatus> = {};
    for (const tr of [...transitions].reverse()) {
        if (tr.fromState !== null && !currentStates[tr.agentType]) {
            currentStates[tr.agentType] = tr.toState;
        }
    }

    return (
        <div className="flex flex-col h-screen overflow-hidden bg-[#0a0d14]">
            {/* ── Top bar ─────────────────────────────────────── */}
            <header className="flex items-center gap-4 px-5 py-3 border-b border-indigo-900/30 bg-[#0d1117] shrink-0">
                <WSLogo />

                <div className="w-px h-7 bg-indigo-900/40 mx-0.5" />

                <NewFlowModal onCreated={id => { setFlowId(id); setTab('graph'); }} />

                <FlowSelector selectedId={flowId} onSelect={id => { setFlowId(id); }} />

                {/* Center tabs */}
                <div className="flex items-center gap-1 bg-[#0a0d14] border border-indigo-900/30 rounded-lg p-0.5 mx-2">
                    {([
                        { id: 'graph', label: 'World State', icon: <RabbitIcon size={14} /> },
                        { id: 'lifecycle', label: 'Lifecycle', icon: <Map size={11} /> },
                        { id: 'transitions', label: 'Agent Transitions', icon: <GitBranch size={11} />, badge: transitions.filter(t => t.fromState !== null).length },
                    ] as const).map(t => (
                        <button
                            key={t.id}
                            onClick={() => setTab(t.id)}
                            className={`flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium transition-all ${
                                tab === t.id
                                    ? 'bg-indigo-600 text-white shadow-sm shadow-indigo-900/50'
                                    : 'text-slate-500 hover:text-slate-300'
                            }`}
                        >
                            {t.icon}
                            {t.label}
                            {'badge' in t && t.badge > 0 && (
                                <span className={`text-[9px] px-1.5 py-0.5 rounded-full font-semibold ${
                                    tab === t.id ? 'bg-white/20 text-white' : 'bg-indigo-900/60 text-indigo-300'
                                }`}>
                                    {t.badge}
                                </span>
                            )}
                        </button>
                    ))}
                </div>

                <div className="ml-auto flex items-center gap-3">
                    {transitions.filter(t => t.fromState !== null).length > 0 && (
                        <div className="flex items-center gap-1.5 text-xs text-emerald-500">
                            <div className="w-1.5 h-1.5 rounded-full bg-emerald-500 animate-pulse" />
                            Live recording
                        </div>
                    )}
                    <a
                        href="https://localhost:8443"
                        target="_blank"
                        rel="noreferrer"
                        className="flex items-center gap-1.5 text-xs text-slate-500 hover:text-slate-300 transition-colors"
                    >
                        <ExternalLink size={11} />
                        Pentest Platform
                    </a>
                </div>
            </header>

            {/* ── Main layout ──────────────────────────────────── */}
            <div className="flex flex-1 overflow-hidden gap-2.5 p-2.5">

                {/* Left col — Agents */}
                <div className="w-[210px] shrink-0">
                    <AgentPanel flowId={flowId} />
                </div>

                {/* Center col — tabbed */}
                <div className="flex-1 min-w-0 flex flex-col gap-2.5">
                    {tab === 'graph' ? (
                        <>
                            <div className="flex-1 min-h-0">
                                {wsModel?.schema_version === 1 ? <WorldStateGraph
                                    entities={entities}
                                    links={links}
                                    loading={wsLoading}
                                    onRefresh={wsRefresh}
                                /> : <CheckModelPanel model={wsModel} loading={wsLoading} error={wsError} onRefresh={wsRefresh} />}
                            </div>
                            <div className="shrink-0">
                                <NextStepPanel flowId={flowId} />
                            </div>
                        </>
                    ) : tab === 'lifecycle' ? (
                        <div className="flex-1 min-h-0">
                            {wsModel?.schema_version === 1 ? <LifecyclePanel key={flowId} flowId={flowId} /> : <CheckModelPanel model={wsModel} loading={wsLoading} error={wsError} onRefresh={wsRefresh} />}
                        </div>
                    ) : (
                        <>
                            <div className="shrink-0">
                                <StateMachineMap currentStates={currentStates} />
                            </div>
                            <div className="flex-1 min-h-0">
                                <AgentStateTimeline transitions={transitions} onClear={clear} />
                            </div>
                        </>
                    )}
                </div>

                {/* Right col — directive feed */}
                <div className="w-[270px] shrink-0">
                    <DirectiveFeed flowId={flowId} />
                </div>
            </div>
        </div>
    );
}

export default function App() {
    return (
        <ApolloProvider client={client}>
            <WorkspaceContent />
        </ApolloProvider>
    );
}
