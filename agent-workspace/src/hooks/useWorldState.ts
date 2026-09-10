import { useState, useEffect, useCallback } from 'react';
import { api, WorldStateModel, WorldStateResponse } from '@/lib/api';

export function useWorldState(flowId: string | null) {
    const [state, setState] = useState<{
        flowId: string | null;
        data: WorldStateResponse | null;
        model: WorldStateModel | null;
        loading: boolean;
        error: string | null;
    }>({ flowId: null, data: null, model: null, loading: false, error: null });
    const [refreshKey, setRefreshKey] = useState(0);
    const refresh = useCallback(() => setRefreshKey((value) => value + 1), []);

    useEffect(() => {
        if (!flowId) return;
        const controller = new AbortController();
        let pending = false;
        const load = async () => {
            if (pending) return;
            pending = true;
            setState((previous) => ({ flowId, data: previous.flowId === flowId ? previous.data : null, model: previous.flowId === flowId ? previous.model : null, loading: true, error: null }));
            try {
                const model = await api.worldStateModel(flowId, controller.signal);
                if (controller.signal.aborted) return;
                const data = model.schema_version === 1 ? await api.worldState(flowId) : null;
                if (!controller.signal.aborted) setState({ flowId, model, data, loading: false, error: null });
            } catch (error) {
                if (!controller.signal.aborted) setState((previous) => ({ ...previous, loading: false, error: error instanceof Error ? error.message : 'Unable to load World State.' }));
            } finally {
                pending = false;
            }
        };
        void load();
        const id = setInterval(load, 6000);
        return () => { controller.abort(); clearInterval(id); };
    }, [flowId, refreshKey]);

    const current = state.flowId === flowId ? state : { data: null, model: null, loading: Boolean(flowId), error: null };
    return { ...current, refresh };
}
