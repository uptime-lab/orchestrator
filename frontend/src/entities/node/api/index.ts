import { getAdoptionRefusals } from './adoptionRefusals.ts'
import { createAdoptionCode } from './createAdoptionCode.ts'
import {
  forgetDeletedNode,
  getDeletedNodes,
  restoreDeletedNode,
} from './deletedNodes.ts'
import { getFavoriteNodeIds, putNodeFavorite } from './favorites.ts'
import { downloadAdoptionFile, getAdoptionFile } from './getAdoptionFile.ts'
import { getBootOptions } from './getBootOptions.ts'
import { getConfigFile, getFile } from './getConfigFile.ts'
import { getConfigFiles, getFiles } from './getConfigFiles.ts'
import {
  getConfigFileVersions,
  getFileVersions,
} from './getConfigFileVersions.ts'
import { getEEPROMBaseFirmware } from './getEEPROMBaseFirmware.ts'
import { getNodeCommandById, getNodeCommands } from './getNodeCommands.ts'
import { getNodes } from './getNodes.ts'
import {
  deleteFileOnDevice,
  getFileReadResult,
  listTrackedFiles,
  refreshFile,
  trackFile,
  untrackFile,
} from './nodeFiles.ts'
import {
  getEEPROMConfigScan,
  getOSUserScan,
  scanEEPROMConfig,
  scanOSUsers,
} from './osUsers.ts'
import { applyOverclockPreset, getOverclockPresets } from './overclock.ts'
import { applyConfigFile, applyFile } from './postApplyFile.ts'
import { approveNode } from './postApproveNode.ts'
import { postBatchNodeCommand } from './postBatchNodeCommand.ts'
import { postClearBootFailsafe } from './postClearBootFailsafe.ts'
import { postCreateNode } from './postCreateNode.ts'
import { deleteNode } from './postDeleteNode.ts'
import { downloadNodeSDImage } from './postDownloadNodeSDImage.ts'
import { postEraseDrive } from './postEraseDrive.ts'
import { postInstallAgent } from './postInstallAgent.ts'
import { postInstallOS } from './postInstallOS.ts'
import { postNodeCommand } from './postNodeCommand.ts'
import { postProvisionConnection } from './postProvisionConnection.ts'
import { provisionEEPROM } from './postProvisionEEPROM.ts'
import { rebootNode } from './postRebootNode.ts'
import { postRebootToRecovery } from './postRebootToRecovery.ts'
import { rejectNode } from './postRejectNode.ts'
import { replaceNode } from './postReplaceNode.ts'
import { shutdownNode } from './postShutdownNode.ts'
import { updateConfigFile, updateFile } from './putUpdateConfigFile.ts'
import { putUpdateNode } from './putUpdateNode.ts'
import { setHostname } from './setHostname.ts'
import { updateAgent } from './updateAgent.ts'
import { updateBootOrder } from './updateBootOrder.ts'

export const apiNodes = {
  getNodes,
  postCreateNode,
  putUpdateNode,
  deleteNode,
  downloadAdoptionFile,
  getAdoptionFile,
  createAdoptionCode,
  rebootNode,
  shutdownNode,
  postInstallOS,
  postInstallAgent,
  postProvisionConnection,
  getFiles,
  getFile,
  getFileVersions,
  updateFile,
  applyFile,
  updateBootOrder,
  postNodeCommand,
  postBatchNodeCommand,
  getNodeCommands,
  getNodeCommandById,
  postEraseDrive,
  getBootOptions,
  approveNode,
  rejectNode,
  provisionEEPROM,
  getEEPROMBaseFirmware,
  updateAgent,
  setHostname,
  getFavoriteNodeIds,
  putNodeFavorite,
  postRebootToRecovery,
  postClearBootFailsafe,
  replaceNode,
  downloadNodeSDImage,
  scanOSUsers,
  getOSUserScan,
  scanEEPROMConfig,
  getEEPROMConfigScan,
  getOverclockPresets,
  applyOverclockPreset,
  listTrackedFiles,
  trackFile,
  refreshFile,
  getFileReadResult,
  untrackFile,
  deleteFileOnDevice,
  getDeletedNodes,
  restoreDeletedNode,
  forgetDeletedNode,
  getAdoptionRefusals,
  // Legacy aliases for backward compatibility
  getConfigFiles,
  getConfigFile,
  getConfigFileVersions,
  updateConfigFile,
  applyConfigFile,
}
