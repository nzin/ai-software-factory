import { client } from './client'

export const listRuns = () => client.get('/v1/runs').then((r) => r.data ?? [])

export const getRun = (id) => client.get(`/v1/runs/${id}`).then((r) => r.data)

export const submitPRD = (body) => client.post('/v1/prd', body).then((r) => r.data)

export const approveRun = (id) => client.post(`/v1/runs/${id}/approve`).then((r) => r.data)

export const rejectRun = (id, body) => client.post(`/v1/runs/${id}/reject`, body).then((r) => r.data)

export const resumeRun = (id, body) => client.post(`/v1/runs/${id}/resume`, body).then((r) => r.data)

export const reviewRun = (id, body) => client.post(`/v1/runs/${id}/review`, body).then((r) => r.data)

export const deleteRun = (id) => client.delete(`/v1/runs/${id}`).then((r) => r.data)
