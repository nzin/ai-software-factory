# Tic-tac-toe web game

Build a small, self-contained tic-tac-toe game: a Go REST backend and a VueJS
frontend, shipped as a single Go binary that also serves the built frontend.

## Background

We want a reference implementation that mirrors the house stack: a Go HTTP API
generated from an OpenAPI 2.0 spec with `go-swagger`, a Vue 3 single-page app for
the UI, and the SPA served statically by the Go process (same origin, one
deployable). Game state lives server-side so the rules are enforced in one place.

## Requirements

### Backend (Go, `go-swagger`)

- API defined spec-first in an OpenAPI 2.0 file; server stubs and models
  generated with `go-swagger`, handlers implemented by hand.
- Endpoints:
  - `POST /api/v1/games` - create a new game, returns a game id and the empty
    board; caller may choose to play `X` or `O` (`X` moves first).
  - `GET /api/v1/games/{id}` - current board, whose turn it is, and status
    (`in_progress`, `x_won`, `o_won`, `draw`).
  - `POST /api/v1/games/{id}/moves` - body `{cell: 0..8}`; applies the human
    move, then the computer plays its move; returns the updated game.
  - `DELETE /api/v1/games/{id}` - discard a game.
- The computer opponent plays a legal move (a simple heuristic is fine: win if
  possible, block if necessary, otherwise take center/corner/side).
- Rules enforced server-side: reject moves on occupied cells, out-of-range cells,
  moves out of turn, or moves after the game is over (`409`).
- In-memory game store is acceptable; games may expire.
- `GET /healthz` liveness endpoint.

### Frontend (Vue 3)

- Vue 3 + Vue Router + a component library, talking to the API with `axios`.
- Screens: a landing screen to start a new game (pick X or O), and a game screen
  with the 3x3 board, current status, a "new game" button, and the move history.
- Clicking an empty cell calls the moves endpoint and re-renders from the
  server's response (never computes game state locally).
- Show win/draw clearly; disable the board when the game is over.

### Packaging

- `make build` produces one Go binary. The Vue app is built (`npm run build`) and
  the resulting `dist/` is served by the Go binary at `/`, with SPA fallback to
  `index.html`; `/api/**` and `/healthz` continue to the API handlers.
- `make dev` (or documented steps) runs the Vue dev server against the Go API for
  local iteration.

## Acceptance criteria

- A fresh checkout runs `make build && ./<binary>` and the game is fully playable
  at `http://localhost:<port>/` with no separate frontend server.
- Playing a full game via the UI reaches a `x_won` / `o_won` / `draw` state and
  the winning line is indicated.
- The API rejects an illegal move (occupied cell, wrong turn, finished game) with
  a `409` and a JSON error body; the UI surfaces it without breaking.
- Backend unit tests cover: win/block/draw detection, illegal-move rejection, and
  the computer never playing an illegal move.
- `go vet` is clean and the generated `go-swagger` code is committed.

## Out of scope

- Accounts, authentication, persistence across restarts.
- Multiplayer / real-time play between two humans.
- An unbeatable (full minimax) AI - a simple heuristic is enough.
- Deployment/CI configuration beyond the single binary.
