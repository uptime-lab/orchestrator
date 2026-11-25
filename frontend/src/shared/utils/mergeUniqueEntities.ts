export const mergeUnique = <T extends { id: string }>(
  initialEntities: T[],
  newEntities: T[],
  sortMethod?: (a: T, b: T) => number,
) => {
  const result = [...initialEntities]
  const entityIdToArrayIndex: Record<string, number> = {}

  initialEntities.forEach(({ id }, arrayIndex) => {
    entityIdToArrayIndex[id] = arrayIndex
  })
  for (const entity of newEntities) {
    if (entity.id in entityIdToArrayIndex) {
      result[entityIdToArrayIndex[entity.id]] = entity
    } else {
      result.push(entity)
    }
  }

  if (initialEntities.length !== result.length && sortMethod) {
    return result.sort(sortMethod)
  }

  return result
}

export const deriveById = <T extends { id: string }>(
  entities: T[],
): Record<string, T> => {
  const entitiesById: Record<string, T> = {}
  for (const entity of entities) {
    entitiesById[entity.id] = entity
  }
  return entitiesById
}

type KeyOfStringOrNumber<T> = {
  [K in keyof T]: T[K] extends infer U
    ? U extends string | number
      ? K
      : never
    : never
}[keyof T]

export const deriveManyByKey = <
  T extends { id: string },
  K extends KeyOfStringOrNumber<T>,
>(
  entities: T[],
  key: K,
): Record<T[K], T[]> => {
  const entitiesById: Record<T[K], T[]> = {}

  for (const entity of entities) {
    const keyValue = entity[key]
    if (!(keyValue in entitiesById)) {
      entitiesById[keyValue] = []
    }
    entitiesById[keyValue].push(entity)
  }
  return entitiesById
}
