import { useQuery, useQueryClient } from '@tanstack/react-query'

import {
  getNodeEffectiveUtilities,
  getUtilityAssignment,
  getUtilityAssignments,
  getUtilityCatalog,
  getUtilityOUCounts,
} from './api'

// One place for the query keys so the page, the config sidebar and the node
// section invalidate the same caches after a mutation.
export const utilityKeys = {
  catalog: ['utility-catalog'] as const,
  assignments: ['utility-assignments'] as const,
  ouCounts: ['utility-ou-counts'] as const,
  assignment: (assignmentId: string) =>
    ['utility-assignment', assignmentId] as const,
  nodeEffective: (nodeId: string) =>
    ['node-effective-utilities', nodeId] as const,
}

export const useUtilityCatalog = () =>
  useQuery({
    queryKey: utilityKeys.catalog,
    queryFn: getUtilityCatalog,
    // Registry content changes only with a deploy (device-profile precedent).
    staleTime: 5 * 60 * 1000,
  })

export const useUtilityAssignments = () =>
  useQuery({
    queryKey: utilityKeys.assignments,
    queryFn: getUtilityAssignments,
  })

export const useUtilityOUCounts = () =>
  useQuery({
    queryKey: utilityKeys.ouCounts,
    queryFn: getUtilityOUCounts,
  })

export const useUtilityAssignment = (assignmentId: string) =>
  useQuery({
    queryKey: utilityKeys.assignment(assignmentId),
    queryFn: () => getUtilityAssignment(assignmentId),
  })

export const useNodeEffectiveUtilities = (nodeId: string) =>
  useQuery({
    queryKey: utilityKeys.nodeEffective(nodeId),
    queryFn: () => getNodeEffectiveUtilities(nodeId),
  })

// Invalidates the list-shaped caches every assignment mutation touches (the
// per-assignment and per-node caches are invalidated by their own callers).
export const useInvalidateUtilityLists = () => {
  const queryClient = useQueryClient()
  return () => {
    void queryClient.invalidateQueries({ queryKey: utilityKeys.assignments })
    void queryClient.invalidateQueries({ queryKey: utilityKeys.ouCounts })
  }
}
