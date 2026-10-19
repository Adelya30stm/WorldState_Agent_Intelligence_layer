import { ChevronDown, Copy, Download, ExternalLink, GripVertical, Loader2, NotepadText } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { toast } from 'sonner';

import { FlowStatusIcon } from '@/components/icons/flow-status-icon';
import { ProviderIcon } from '@/components/icons/provider-icon';
import { Breadcrumb, BreadcrumbItem, BreadcrumbList, BreadcrumbPage } from '@/components/ui/breadcrumb';
import { Button } from '@/components/ui/button';
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from '@/components/ui/resizable';
import FlowCentralTabs from '@/features/flows/flow-central-tabs';
import FlowTabs from '@/features/flows/flow-tabs';
import { ScopeConfirmDialog } from '@/features/world-state/scope-confirm-dialog';
import { useWorldStateToolCallPopup } from '@/features/world-state/use-world-state-toolcall-popup';
import WorldStateView from '@/features/world-state/world-state-view';
import { usePutUserInputMutation } from '@/graphql/types';
import { useBreakpoint } from '@/hooks/use-breakpoint';
import { Log } from '@/lib/log';
import { copyToClipboard, downloadTextFile, generateFileName, generateReport } from '@/lib/report';
import { formatName } from '@/lib/utils/format';
import { useFlow } from '@/providers/flow-provider';

const FlowReportDropdown = () => {
    const { flowData, flowId } = useFlow();
    const flow = flowData?.flow;
    const tasks = flowData?.tasks ?? [];

    // Check if flow is available for report generation
    const isReportDisabled = !flow || !flowId;

    // Report export handlers
    const handleCopyToClipboard = async () => {
        if (isReportDisabled) {
            return;
        }

        const reportContent = generateReport(tasks, flow);
        const success = await copyToClipboard(reportContent);

        if (success) {
            toast.success('Report copied to clipboard');
        } else {
            Log.error('Failed to copy report to clipboard');
            toast.error('Failed to copy report to clipboard');
        }
    };

    const handleDownloadMD = () => {
        if (isReportDisabled || !flow) {
            return;
        }

        try {
            // Generate report content
            const reportContent = generateReport(tasks, flow);

            // Generate file name
            const baseFileName = generateFileName(flow);
            const fileName = `${baseFileName}.md`;

            // Download file
            downloadTextFile(reportContent, fileName, 'text/markdown; charset=UTF-8');
        } catch (error) {
            Log.error('Failed to download markdown report:', error);
        }
    };

    const handleDownloadPDF = () => {
        if (isReportDisabled || !flow || !flowId) {
            return;
        }

        // Open new tab (not popup) with report page and download flag
        const url = `/flows/${flowId}/report?download=true&silent=true`;
        window.open(url, '_blank');
    };

    const handleOpenWebView = () => {
        if (isReportDisabled || !flowId) {
            return;
        }

        // Open new tab with report page for web viewing
        const url = `/flows/${flowId}/report`;
        window.open(url, '_blank');
    };

    return (
        <DropdownMenu>
            <DropdownMenuTrigger asChild>
                <Button
                    className="shrink-0"
                    disabled={isReportDisabled}
                    variant="ghost"
                >
                    <NotepadText />
                    Report
                    <ChevronDown className="opacity-50" />
                </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
                <DropdownMenuItem
                    className="flex items-center gap-2"
                    disabled={isReportDisabled}
                    onClick={handleOpenWebView}
                >
                    <ExternalLink className="size-4" />
                    Open web view
                </DropdownMenuItem>
                <DropdownMenuItem
                    className="flex items-center gap-2"
                    disabled={isReportDisabled}
                    onClick={handleCopyToClipboard}
                >
                    <Copy className="size-4" />
                    Copy to clipboard
                </DropdownMenuItem>
                <DropdownMenuItem
                    className="flex items-center gap-2"
                    disabled={isReportDisabled}
                    onClick={handleDownloadMD}
                >
                    <Download className="size-4" />
                    Download MD
                </DropdownMenuItem>
                <DropdownMenuItem
                    className="flex items-center gap-2"
                    disabled={isReportDisabled}
                    onClick={handleDownloadPDF}
                >
                    <Download className="size-4" />
                    Download PDF
                </DropdownMenuItem>
            </DropdownMenuContent>
        </DropdownMenu>
    );
};

const Flow = () => {
    const { isDesktop } = useBreakpoint();
    const navigate = useNavigate();
    const [searchParams, setSearchParams] = useSearchParams();

    // Get flow data from FlowProvider
    const { flowData, flowError, flowId, isLoading: isFlowLoading } = useFlow();

    // Pop a toast each time an agent calls a World State tool during the flow.
    useWorldStateToolCallPopup(flowId);

    // Redirect to flows list if there's an error loading flow data or flow not found
    useEffect(() => {
        if (flowError || (!isFlowLoading && !flowData?.flow)) {
            navigate('/flows', { replace: true });
        }
    }, [flowError, flowData, isFlowLoading, navigate]);

    // Auto-send phase prompt when navigated from web-pentest with ?prompt=
    const [putUserInput] = usePutUserInputMutation();
    const autoPromptRef = useRef(false);
    useEffect(() => {
        const prompt = searchParams.get('prompt');

        if (!prompt || !flowId || autoPromptRef.current) {return;}

        autoPromptRef.current = true;
        putUserInput({ variables: { flowId, input: prompt } });
        setSearchParams({}, { replace: true });
    }, [flowId, searchParams, setSearchParams, putUserInput]);

    const [activeTabsTab, setActiveTabsTab] = useState<string>(!isDesktop ? 'automation' : 'terminal');
    const [leftTab, setLeftTab] = useState<string>(() => {
        const tab = searchParams.get('tab');
        if (tab === 'assistant' || tab === 'world-state' || tab === 'automation') {
            return tab;
        }
        return 'automation';
    });

    const isWorldState = isDesktop && leftTab === 'world-state';
    const confirmScope = searchParams.get('confirmScope') === '1';

    const clearConfirmScope = () => {
        setSearchParams(
            (prev) => {
                const next = new URLSearchParams(prev);
                next.delete('confirmScope');
                return next;
            },
            { replace: true },
        );
    };

    const scopeDialog =
        flowId != null ? (
            <ScopeConfirmDialog flowId={flowId} onClose={clearConfirmScope} open={confirmScope} />
        ) : null;

    const tabsCard = (
        <div className="flex h-[calc(100dvh-3rem)] max-w-full flex-col rounded-none border-0">
            <div className="flex-1 overflow-auto py-4 pl-4 pr-0">
                <FlowTabs
                    activeTab={activeTabsTab}
                    onTabChange={setActiveTabsTab}
                />
            </div>
        </div>
    );

    const header = (
        <header className="sticky top-0 z-10 flex h-12 w-full shrink-0 items-center gap-2 border-b bg-background transition-[width,height] ease-linear group-has-data-[collapsible=icon]/sidebar-wrapper:h-12">
            <div className="flex w-full items-center justify-between gap-2 px-4">
                <div className="flex items-center gap-2">
                    <Breadcrumb>
                        <BreadcrumbList>
                            <BreadcrumbItem className="gap-2">
                                {flowData?.flow && (
                                    <>
                                        <FlowStatusIcon
                                            status={flowData.flow.status}
                                            tooltip={formatName(flowData.flow.status)}
                                        />
                                        <ProviderIcon
                                            provider={flowData.flow.provider}
                                            tooltip={formatName(flowData.flow.provider.name)}
                                        />
                                    </>
                                )}
                                <BreadcrumbPage>{flowData?.flow?.title || 'Select a flow'}</BreadcrumbPage>
                            </BreadcrumbItem>
                        </BreadcrumbList>
                    </Breadcrumb>
                </div>
                {!!(flowData?.tasks ?? [])?.length && <FlowReportDropdown />}
            </div>
        </header>
    );

    // ── World State full-width mode ────────────────────────────────────────────
    if (isWorldState) {
        return (
            <>
                {header}
                {scopeDialog}
                <div className="relative flex h-[calc(100dvh-3rem)] w-full max-w-full flex-1">
                    {isFlowLoading && (
                        <div className="absolute inset-0 z-50 flex items-center justify-center bg-background/50">
                            <Loader2 className="size-16 animate-spin" />
                        </div>
                    )}
                    {/* Tab strip at top so user can switch back */}
                    <div className="flex h-full w-full flex-col">
                        <div className="shrink-0 px-4 pt-3 pb-0 border-b">
                            <div className="flex gap-1">
                                {['automation', 'assistant', 'world-state'].map((tab) => (
                                    <button
                                        className={`rounded-t-md px-3 py-1.5 text-sm font-medium transition-colors border-b-2 ${
                                            tab === 'world-state'
                                                ? 'border-primary text-foreground'
                                                : 'border-transparent text-muted-foreground hover:text-foreground'
                                        }`}
                                        key={tab}
                                        onClick={() => setLeftTab(tab)}
                                    >
                                        {tab === 'world-state' ? 'World State' : tab.charAt(0).toUpperCase() + tab.slice(1)}
                                    </button>
                                ))}
                            </div>
                        </div>
                        <div className="flex-1 overflow-hidden">
                            <WorldStateView onBack={() => setLeftTab('automation')} />
                        </div>
                    </div>
                </div>
            </>
        );
    }

    // ── Normal split layout ───────────────────────────────────────────────────
    return (
        <>
            {header}
            {scopeDialog}
            <div className="relative flex h-[calc(100dvh-3rem)] w-full max-w-full flex-1">
                {isFlowLoading && (
                    <div className="absolute inset-0 z-50 flex items-center justify-center bg-background/50">
                        <Loader2 className="size-16 animate-spin" />
                    </div>
                )}
                {isDesktop ? (
                    <ResizablePanelGroup className="w-full" direction="horizontal">
                        <ResizablePanel defaultSize={50} minSize={30}>
                            <div className="flex h-[calc(100dvh-3rem)] max-w-full flex-col rounded-none border-0">
                                <div className="flex-1 overflow-auto py-4 pl-4 pr-0">
                                    <FlowCentralTabs
                                        onTabChange={setLeftTab}
                                        value={leftTab}
                                    />
                                </div>
                            </div>
                        </ResizablePanel>
                        <ResizableHandle withHandle>
                            <GripVertical className="size-4" />
                        </ResizableHandle>
                        <ResizablePanel defaultSize={50} minSize={30}>
                            {tabsCard}
                        </ResizablePanel>
                    </ResizablePanelGroup>
                ) : (
                    tabsCard
                )}
            </div>
        </>
    );
};

export default Flow;
