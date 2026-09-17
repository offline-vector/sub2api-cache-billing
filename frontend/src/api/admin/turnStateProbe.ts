import { apiClient } from '../client'

export interface TurnStateProbeEvent {
  id: string
  time: string
  event?: string
  account_id?: number
  model?: string
  route?: string
  attempt?: number
  sent_state_length?: number
  received_state_length?: number
  http_status?: number
  elapsed_s?: number
  completed: boolean
  response_model?: string
  error_code?: string
  error_type?: string
  transient_error?: string
  upstream_server?: string
  retry_after_s?: number
  wait_seconds?: number
  cross_session_check?: boolean
  shared_state_published?: boolean
  verification_output_matches?: boolean
}

export interface TurnStateProbeLogPage {
  status: {
    running: boolean
    updated_at: string
    account_ids: number[]
    models: string[]
    concurrency: number
    interval_seconds: number
  } | null
  items: TurnStateProbeEvent[]
  next_before?: string
  retention_limit: number
}

export function listTurnStateProbeLogs(
  params: { account_id?: number; model?: string; before?: string },
  signal?: AbortSignal
): Promise<TurnStateProbeLogPage> {
  return apiClient.get<TurnStateProbeLogPage>('/admin/usage/probe-logs', { params, signal })
    .then(({ data }) => data)
}
