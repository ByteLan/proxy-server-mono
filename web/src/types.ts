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
  url?: string;
  http_url?: string;
  created_at: number;
  updated_at: number;
}

export type SubscriptionInput = Pick<Subscription, 'name' | 'node_ids' | 'protocols' | 'routes' | 'enabled'>;

export interface OnboardingDefaults {
  public_url: string;
  http_sub_public_url?: string;
  enroll_token: string;
  ca_cert_available: boolean;
  ca_cert_pem?: string;
  version: string;
  default_repo: string;
}
