const BASE = '/api/v1';

async function req<T>(path: string, opts?: RequestInit): Promise<T> {
    const res = await fetch(`${BASE}${path}`, {
        credentials: 'include',
        headers: { 'Content-Type': 'application/json', ...opts?.headers },
        ...opts,
    });
    if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
    const json = await res.json();
    return (json.data ?? json) as T;
}

export const api = {
    flows: () => req<Flow[]>('/flows/'),
    worldState: (flowId: string) => req<WorldStateResponse>(`/flows/${flowId}/worldstate`),
    worldStateModel: (flowId: string, signal?: AbortSignal) => req<WorldStateModel>(`/flows/${flowId}/worldstate/model`, { signal }),
    lifecycle: (flowId: string) => req<LifecycleResponse>(`/flows/${flowId}/worldstate/lifecycle`),
    exec: (flowId: string, command: string) =>
        req<ExecResult>(`/flows/${flowId}/exec/`, { method: 'POST', body: JSON.stringify({ command }) }),
    nextstep: (flowId: string) => req<NextStepResponse>(`/flows/${flowId}/nextstep/`),
};

export const GQL_ENDPOINT = '/api/v1/graphql';
export const WS_ENDPOINT = typeof window !== 'undefined'
    ? `wss://${window.location.host}/api/v1/graphql`
    : 'wss://localhost:8443/api/v1/graphql';

// Types
export interface Flow {
    id: string;
    title: string;
    status: 'created' | 'waiting' | 'running' | 'finished' | 'failed';
    createdAt: string;
    providerName: string;
}

export interface WorldStateEntity {
    id: string;
    type: string;
    label: string;
    riskLevel: string;
    metadata: Record<string, string>;
}

export interface WorldStateLink {
    id: string;
    source: string;
    target: string;
    label?: string;
    type: string;
}

export interface WorldStateResponse {
    entities: WorldStateEntity[];
    links: WorldStateLink[];
    flowId: number;
}

export interface WorldStateModel {
    schema_version: number;
    flow_id: number;
    revision: number;
    object_count: number;
    check_count: number;
    run_count: number;
    objects: WorldStateObject[];
}

export interface WorldStateObject {
    id: number;
    object_key: string;
    type: string;
    locator: Record<string, unknown>;
    computed_status: string;
    check_count: number;
    run_count: number;
    checks: WorldStateCheck[];
}

export interface WorldStateCheck {
    id: number;
    kind: string;
    computed_status: string;
    run_count: number;
    runs: WorldStateRun[];
}

export interface WorldStateRun {
    id: number;
    tool_call_id: number | null;
    parameters: Record<string, unknown>;
    evidence: Record<string, unknown>;
    execution_status: string;
    verdict: string | null;
    created_at: string;
    started_at: string | null;
    finished_at: string | null;
}

export interface LifecycleEntity {
    id: number;
    entityKey: string;
    type: string;
    state: string;
    properties: Record<string, unknown>;
    updatedAt: string;
}

export interface LifecycleTransition {
    id: number;
    entityId: number;
    entityKey: string;
    fromState: string;
    toState: string;
    agent: string;
    evidence: Record<string, unknown>;
    createdAt: string;
}

export interface LifecycleResponse {
    flowId: number;
    entities: LifecycleEntity[];
    transitions: LifecycleTransition[];
    snapshot: string;
    counts: Record<string, number>;
}

export interface ExecResult {
    flowId: number;
    container: string;
    command: string;
    output: string;
    exitCode: number;
}

export interface NextStepRec {
    step: string;
    rationale: string;
    priority: string;
    phase: string;
    command: string;
}

export interface NextStepResponse {
    flowId: number;
    currentPhase: string;
    summary: string;
    recommendations: NextStepRec[];
    caution: string;
}
