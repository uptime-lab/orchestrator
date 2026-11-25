export const WEBSOCKET_SHUTDOWN_CODE = 4242

export enum UserSocketEventType {
  AuthorizationOpened = 'authorization_status',
  Health = 'health',
  NodeCreated = 'node_created',
  NodeUpdated = 'node_updated',
  NodeDeleted = 'node_deleted',
  ImageCreated = 'image_created',
  ImageUpdated = 'image_updated',
  ImageDeleted = 'image_deleted',
  NodeCommandCreated = 'node_command_created',
  NodeCommandUpdated = 'node_command_updated',
  NodeMetrics = 'node_metrics',
  WorkflowExecutionUpdated = 'workflow_execution_updated',
  WorkflowExecutionNodeUpdated = 'workflow_execution_node_updated',
  // A device tried to adopt and was refused. Pushed because the device itself
  // just retries in silence — without this the person waiting on the Add Node
  // screen sees nothing at all.
  NodeAdoptionRefused = 'node_adoption_refused',
  // A human pressed the physical button on a node (device profiles): the
  // device-to-web half of identify. Transient — nothing is persisted.
  NodeButtonPressed = 'node_button_pressed',
}
