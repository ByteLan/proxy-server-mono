export interface Endpoint {
  id: string;
  name: string;
  type: string;
  server: string;
  port: number;
}

export interface CoreStatus {
  name: string;
  running: boolean;
  version?: string;
  error?: string;
}

export interface ProxyNode {
  id: string;
  name: string;
  os: string;
  arch: string;
  version: string;
  enabled: boolean;
  last_seen: number;
  online: boolean;
  config_hash: string;
  cores: CoreStatus[];
  endpoints: Endpoint[];
}

export interface RouteGroup {
  custom: boolean;
  node_ids: string[];
}

export interface SiteRoutes {
  claude: RouteGroup;
  chatgpt: RouteGroup;
  google: RouteGroup;
}

export interface Subscription {
  id: string;
  name: string;
  node_ids: string[];
  protocols: string[];
  routes: SiteRoutes;
  enabled: boolean;
  created_at: number;
  updated_at: number;
}

export type SubscriptionInput = Pick<Subscription, 'name' | 'node_ids' | 'protocols' | 'routes' | 'enabled'>;
