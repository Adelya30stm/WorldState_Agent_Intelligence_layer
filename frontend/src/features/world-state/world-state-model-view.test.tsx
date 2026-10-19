import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';

import type { WorldStateModel, WorldStateRun } from './world-state-model-types';
import { WorldStateModelView } from './world-state-model-view';

const time = '2026-09-10T12:00:00Z';

function run(id: number, verdict: WorldStateRun['verdict']): WorldStateRun {
    return {
        id,
        check_id: 10,
        tool_call_id: null,
        parameters: { payload: '<svg onload="alert(1)">' },
        execution_status: 'completed',
        verdict,
        evidence: { response_excerpt: '<script>alert(1)</script>' },
        created_at: time,
        started_at: time,
        finished_at: time,
    };
}

function snapshot(): WorldStateModel {
    return {
        schema_version: 2,
        flow_id: 1,
        revision: 9,
        object_count: 1,
        check_count: 2,
        run_count: 3,
        objects: [
            {
                id: 1,
                flow_id: 1,
                object_key: 'parameter:GET:https://example.test/search:query:q',
                type: 'parameter',
                locator: { url: 'https://example.test/search', method: 'GET', location: 'query', name: 'q' },
                computed_status: 'vulnerable',
                check_count: 2,
                run_count: 3,
                created_at: time,
                updated_at: time,
                checks: [
                    {
                        id: 10,
                        object_id: 1,
                        kind: 'xss',
                        computed_status: 'vulnerable',
                        run_count: 2,
                        created_at: time,
                        updated_at: time,
                        runs: [run(101, 'vulnerable'), run(102, 'not_reproduced')],
                    },
                    {
                        id: 11,
                        object_id: 1,
                        kind: 'sqli',
                        computed_status: 'not_confirmed',
                        run_count: 1,
                        created_at: time,
                        updated_at: time,
                        runs: [{ ...run(103, 'not_reproduced'), check_id: 11 }],
                    },
                ],
            },
        ],
    };
}

describe('World State model display', () => {
    it('keeps repeated experiments separate and renders payloads/evidence as text', () => {
        const html = renderToStaticMarkup(<WorldStateModelView model={snapshot()} />);
        expect(html).toContain('Run #101');
        expect(html).toContain('Run #102');
        expect(html).toContain('Run #103');
        expect(html).toContain('Not reproduced');
        expect(html).toContain('&lt;script&gt;alert(1)&lt;/script&gt;');
        expect(html).not.toContain('<script>');
        expect(html).not.toContain('<svg onload=');
        expect(html).toContain('Object location');
    });

    it('shows only confirmed checks in findings, retaining their negative experiments', () => {
        const html = renderToStaticMarkup(
            <WorldStateModelView
                model={snapshot()}
                findingsOnly
            />,
        );
        expect(html).toContain('xss');
        expect(html).toContain('Run #101');
        expect(html).toContain('Run #102');
        expect(html).not.toContain('Run #103');
        expect(html).not.toContain('>sqli<');
    });

    it('does not label an empty model as secure or infer findings from other data', () => {
        const model = { ...snapshot(), objects: [], object_count: 0, check_count: 0, run_count: 0 };
        const html = renderToStaticMarkup(<WorldStateModelView model={model} />);
        expect(html).toContain('No objects recorded yet.');
        expect(html).not.toContain('Vulnerable');
        expect(html).not.toContain('secure');
    });
});
