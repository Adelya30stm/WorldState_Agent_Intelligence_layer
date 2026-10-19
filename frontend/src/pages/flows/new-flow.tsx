import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';

import { Breadcrumb, BreadcrumbItem, BreadcrumbList, BreadcrumbPage } from '@/components/ui/breadcrumb';
import { Card, CardContent } from '@/components/ui/card';
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { FlowForm, type FlowFormValues } from '@/features/flows/flow-form';
import { useFlows } from '@/providers/flows-provider';
import { useProviders } from '@/providers/providers-provider';
import { useSystemSettings } from '@/providers/system-settings-provider';

const NewFlow = () => {
    const navigate = useNavigate();
    const [searchParams] = useSearchParams();

    const { selectedProvider } = useProviders();
    const { createFlow, createFlowWithAssistant } = useFlows();
    const { settings } = useSystemSettings();

    // Auto-submit when ?prompt= is provided (e.g. from Phases / Planning)
    const autoPrompt = searchParams.get('prompt');
    const autoSubmittedRef = useRef(false);

    const [isLoading, setIsLoading] = useState(false);
    // Phase launches must start in assistant mode so use_agents runs.
    const [flowType, setFlowType] = useState<'assistant' | 'automation'>(
        autoPrompt ? 'assistant' : 'automation',
    );

    // Calculate default useAgents value (only for assistant type)
    const shouldUseAgents = useMemo(() => {
        return settings?.assistantUseAgents ?? false;
    }, [settings?.assistantUseAgents]);

    const handleSubmit = async (values: FlowFormValues) => {
        if (isLoading) {
            return;
        }

        setIsLoading(true);

        try {
            const flowId = flowType === 'automation' ? await createFlow(values) : await createFlowWithAssistant(values);

            if (flowId) {
                const webPentestTarget = searchParams.get('webPentestTarget');
                if (webPentestTarget) {
                    localStorage.setItem('webPentestFlow', JSON.stringify({ flowId, target: webPentestTarget }));
                }
                navigate(`/flows/${flowId}?tab=${flowType}&confirmScope=1`);
            }
        } finally {
            setIsLoading(false);
        }
    };

    // Auto-submit if ?prompt= is in the URL (launched from Phases / Planning)
    useEffect(() => {
        if (!autoPrompt || autoSubmittedRef.current || isLoading || !selectedProvider) return;
        autoSubmittedRef.current = true;
        handleSubmit({
            message: autoPrompt,
            providerName: selectedProvider.name,
            useAgents: true,
        });
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [autoPrompt, selectedProvider]);

    // When launched with ?prompt= (e.g. from Planning / Phases), skip the form UI entirely
    if (autoPrompt) {
        return (
            <div className="flex min-h-dvh items-center justify-center gap-3 text-sm text-muted-foreground">
                <svg className="size-5 animate-spin text-blue-600" fill="none" viewBox="0 0 24 24">
                    <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                    <path className="opacity-75" d="M4 12a8 8 0 018-8v4a4 4 0 00-4 4H4z" fill="currentColor" />
                </svg>
                Preparing flow…
            </div>
        );
    }

    return (
        <>
            <header className="sticky top-0 z-10 flex h-12 shrink-0 items-center gap-2 border-b bg-background px-4">
                <Breadcrumb>
                    <BreadcrumbList>
                        <BreadcrumbItem>
                            <BreadcrumbPage>New flow</BreadcrumbPage>
                        </BreadcrumbItem>
                    </BreadcrumbList>
                </Breadcrumb>
            </header>
            <div className="flex min-h-[calc(100dvh-3rem)] items-center justify-center p-4">
                <Card className="w-full max-w-2xl">
                    <CardContent className="flex flex-col gap-4 pt-6">
                        <div className="text-center">
                            <h1 className="text-2xl font-semibold">Create a new flow</h1>
                            <p className="mt-2 text-muted-foreground">Describe what you would like to test</p>
                        </div>
                        <Tabs
                            onValueChange={(value) => setFlowType(value as 'assistant' | 'automation')}
                            value={flowType}
                        >
                            <TabsList className="grid w-full grid-cols-2">
                                <TabsTrigger
                                    disabled={isLoading}
                                    value="automation"
                                >
                                    Automation
                                </TabsTrigger>
                                <TabsTrigger
                                    disabled={isLoading}
                                    value="assistant"
                                >
                                    Assistant
                                </TabsTrigger>
                            </TabsList>
                        </Tabs>
                        <FlowForm
                            defaultValues={{
                                message: '',
                                providerName: selectedProvider?.name ?? '',
                                useAgents: shouldUseAgents,
                                scopeDomains: '',
                            }}
                            isSubmitting={isLoading}
                            onSubmit={handleSubmit}
                            placeholder={
                                !isLoading
                                    ? flowType === 'automation'
                                        ? 'Describe what you would like to test...'
                                        : 'What would you like me to help you with?'
                                    : 'Creating a new flow...'
                            }
                            showScope
                            type={flowType}
                        />
                    </CardContent>
                </Card>
            </div>
        </>
    );
};

export default NewFlow;
