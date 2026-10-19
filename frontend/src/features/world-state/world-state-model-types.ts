export type WorldStateComputedStatus = 'unknown' | 'in_progress' | 'vulnerable' | 'not_confirmed' | 'inconclusive';
export type WorldStateExecutionStatus = 'planned' | 'running' | 'completed' | 'failed' | 'cancelled';
export type WorldStateVerdict = 'vulnerable' | 'not_reproduced' | 'inconclusive';

export interface WorldStateRun {
    id: number;
    check_id: number;
    tool_call_id: number | null;
    parameters: Record<string, unknown>;
    execution_status: WorldStateExecutionStatus;
    verdict: WorldStateVerdict | null;
    evidence: Record<string, unknown>;
    created_at: string;
    started_at: string | null;
    finished_at: string | null;
}

export interface WorldStateCheck {
    id: number;
    object_id: number;
    kind: string;
    computed_status: WorldStateComputedStatus;
    run_count: number;
    created_at: string;
    updated_at: string;
    runs: WorldStateRun[];
}

export interface WorldStateObject {
    id: number;
    flow_id: number;
    object_key: string;
    type: string;
    locator: Record<string, unknown>;
    computed_status: WorldStateComputedStatus;
    check_count: number;
    run_count: number;
    created_at: string;
    updated_at: string;
    checks: WorldStateCheck[];
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
