import { useCallback, useEffect, useState } from 'react';

import { axios } from '@/lib/axios';

import type { WorldStateModel } from './world-state-model-types';

export async function fetchWorldStateModel(flowId: string, signal?: AbortSignal): Promise<WorldStateModel> {
    const body = await axios.get<never, { data: WorldStateModel }>(`/flows/${flowId}/worldstate/model`, { signal });
    return body.data;
}

/** Version discovery and live data come from the same authorized snapshot endpoint. */
export function useWorldStateModel(flowId: string | null) {
    const [state, setState] = useState<{
        flowId: string | null;
        data: WorldStateModel | null;
        error: string | null;
        loading: boolean;
    }>({ flowId: null, data: null, error: null, loading: false });
    const [refreshKey, setRefreshKey] = useState(0);
    const refresh = useCallback(() => setRefreshKey((value) => value + 1), []);

    useEffect(() => {
        if (!flowId) return;
        const controller = new AbortController();
        let pending = false;
        const load = async () => {
            if (pending) return;
            pending = true;
            setState((previous) => ({
                flowId,
                data: previous.flowId === flowId ? previous.data : null,
                error: null,
                loading: true,
            }));
            try {
                const data = await fetchWorldStateModel(flowId, controller.signal);
                if (!controller.signal.aborted) setState({ flowId, data, loading: false, error: null });
            } catch {
                if (!controller.signal.aborted) {
                    setState((previous) => ({ ...previous, loading: false, error: 'Unable to load World State.' }));
                }
            } finally {
                pending = false;
            }
        };
        void load();
        const interval = setInterval(load, 6000);
        return () => {
            controller.abort();
            clearInterval(interval);
        };
    }, [flowId, refreshKey]);

    const current = state.flowId === flowId ? state : { data: null, error: null, loading: Boolean(flowId) };
    return { data: current.data, error: current.error, loading: current.loading, refresh };
}
