import { ProviderType } from '@/graphql/types';

export interface Provider {
    name: string;
    type: ProviderType;
}

/**
 * Generates a display name for a provider
 * If the name matches the type, only the name is returned
 * Otherwise, returns "name - type"
 */
export const getProviderDisplayName = (provider: Provider): string => {
    return provider.name;
};

/**
 * Checks if a provider exists in the list of providers
 */
export const isProviderValid = (provider: Provider, providers: Provider[]): boolean => {
    return providers.some((p) => p.name === provider.name && p.type === provider.type);
};

/**
 * Finds a provider by name and type
 */
export const findProvider = (provider: Provider, providers: Provider[]): Provider | undefined => {
    return providers.find((p) => p.name === provider.name && p.type === provider.type);
};

/**
 * Finds a provider by name
 */
export const findProviderByName = (providerName: string, providers: Provider[]): Provider | undefined => {
    return providers.find((provider) => provider.name === providerName);
};

/**
 * Sorts providers with OpenAI first, Anthropic second, then alphabetically
 */
export const sortProviders = (providers: Provider[]): Provider[] => {
    const rank = (name: string) => {
        if (name === 'openai') return 0;
        if (name === 'anthropic') return 1;
        return 2;
    };

    return [...providers].sort((a, b) => {
        const rankDiff = rank(a.name) - rank(b.name);
        if (rankDiff !== 0) return rankDiff;
        return a.name.localeCompare(b.name);
    });
};
