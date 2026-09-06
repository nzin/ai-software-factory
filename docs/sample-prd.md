# Add a "favorite agents" list to the catalog UI

Operators who run many specialized agents want to pin the ones they touch most
often so they surface at the top of the roster.

## Background

The catalog already exposes every registered agent through `GET /v1/agents`.
There is no notion of per-operator preferences yet.

## Requirements

- An operator can mark an agent as a favorite and unmark it.
- Favorites are per-operator, not global.
- The agent list returns favorites first, then everything else, each group
  sorted by role.
- Favoriting a non-existent agent is a 404.

## Acceptance criteria

- `PUT /v1/agents/{role}/favorite` and `DELETE /v1/agents/{role}/favorite`
  toggle the flag for the calling operator.
- `GET /v1/agents?favorites=true` returns only favorites.
- The default `GET /v1/agents` ordering puts favorites first.
- Unit tests cover the ordering and the per-operator isolation.

## Out of scope

- Any UI work beyond wiring the new endpoints.
- Sharing favorite lists between operators.
