import { client } from './client'

export const listAgents = () => client.get('/v1/agents').then((r) => r.data ?? [])

export const getAgent = (role) => client.get(`/v1/agents/${role}`).then((r) => r.data)
