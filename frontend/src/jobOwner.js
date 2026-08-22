const shortOwner = (id) => (id ? `${id.slice(0, 8)}…` : '—')

// Job records retain the immutable identity subject for authorization. The
// browser resolves that opaque value through the administrator's user
// directory before presenting it to people.
export function displayJobOwner(ownerID, currentUserID, usersByID) {
  if (ownerID && ownerID === currentUserID) return '我'
  const username = usersByID?.get(ownerID)
  return username || shortOwner(ownerID)
}
