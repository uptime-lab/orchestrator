// we can use such a hash to assign constant properties to an user entities.
// e.g. assigning avatar color
export const getUuidHash = (uuid: string, N = 7) => {
  const INIT_NUMBER = 271
  const x = uuid
    .split('-')
    .reduce((a, b) => a ^ Number.parseInt(b, 16), INIT_NUMBER)
  return Math.abs(x) % N
}
