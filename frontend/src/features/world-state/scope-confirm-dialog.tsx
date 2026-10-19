import { useEffect, useState } from 'react';
import { toast } from 'sonner';

import { Button } from '@/components/ui/button';
import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
} from '@/components/ui/dialog';
import { Label } from '@/components/ui/label';
import { Textarea } from '@/components/ui/textarea';
import { axios } from '@/lib/axios';

interface ScopeBody {
    cidrs?: string[];
    domains?: string[];
}

function unwrapScope(body: { data?: { scope?: ScopeBody }; scope?: ScopeBody }): ScopeBody {
    return body.data?.scope ?? body.scope ?? {};
}

function parseDomains(raw: string): string[] {
    return raw
        .split(/[,;\n]+/)
        .map((part) => part.trim())
        .filter(Boolean);
}

export function ScopeConfirmDialog({
    flowId,
    onClose,
    open,
}: {
    flowId: string;
    onClose: () => void;
    open: boolean;
}) {
    const [text, setText] = useState('');
    const [saving, setSaving] = useState(false);

    useEffect(() => {
        if (!open) {
            return;
        }

        let cancelled = false;

        (async () => {
            try {
                const body = (await axios.get(`/flows/${flowId}/worldstate/scope`)) as {
                    data?: { scope?: ScopeBody };
                    scope?: ScopeBody;
                };
                if (cancelled) {
                    return;
                }
                const domains = unwrapScope(body).domains ?? [];
                setText(domains.join(', '));
            } catch {
                /* empty field — user types the list */
            }
        })();

        return () => {
            cancelled = true;
        };
    }, [flowId, open]);

    const confirm = async () => {
        if (saving) {
            return;
        }
        setSaving(true);
        try {
            await axios.put(`/flows/${flowId}/worldstate/scope`, {
                cidrs: [],
                domains: parseDomains(text),
            });
            onClose();
        } catch (e) {
            toast.error(e instanceof Error ? e.message : 'Failed to save scope');
        } finally {
            setSaving(false);
        }
    };

    return (
        <Dialog open={open}>
            <DialogContent
                className="sm:max-w-lg [&>button]:hidden"
                onEscapeKeyDown={(event) => event.preventDefault()}
                onPointerDownOutside={(event) => event.preventDefault()}
            >
                <DialogHeader>
                    <DialogTitle>Confirm in-scope domains</DialogTitle>
                    <DialogDescription>
                        Only these hosts are written to World State. Lookalikes (other TLDs, typos) wait for a batch
                        review. Leave empty to ingest without a scope filter.
                    </DialogDescription>
                </DialogHeader>
                <div className="grid gap-2">
                    <Label htmlFor="scope-domains">In scope</Label>
                    <Textarea
                        id="scope-domains"
                        minHeight={72}
                        onChange={(event) => setText(event.target.value)}
                        placeholder="mokka.ru, shop.mokka.ru"
                        value={text}
                    />
                </div>
                <DialogFooter>
                    <Button disabled={saving} onClick={onClose} type="button" variant="outline">
                        Skip
                    </Button>
                    <Button disabled={saving} onClick={confirm} type="button">
                        Confirm scope
                    </Button>
                </DialogFooter>
            </DialogContent>
        </Dialog>
    );
}
