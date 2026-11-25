export type FileStorageInfo = {
  storage_type: string
  file_path: string
  file_name?: string
  size_bytes?: number
  uploaded_at?: string // RFC3339
  // Pre-compression original kept in storage as a safety copy
  source_file_path?: string
  source_size_bytes?: number
}

// Image kinds: os = uploaded OS image, golden = generalized capture
// (deployable onto any device), backup = byte-exact capture of one node.
export type ImageKind = 'os' | 'golden' | 'backup'

export type ImageResult = {
  id: string
  account_id: string
  title: string
  version?: string
  description?: string
  image_url?: string
  kind?: ImageKind
  // Node a golden/backup image was captured from
  source_node_id?: string
  source_node_name?: string
  file_storage_info?: FileStorageInfo // File is uploaded if this exists
  checksum?: string // SHA256 of the uploaded file
  // 'uploading' marks a record whose file is being replaced right now. It is a
  // claim the backend holds for the whole transfer so a compression cannot start
  // inside it, which means another viewer sees it while somebody
  // else uploads — the uploader's own client is watching its own progress bar.
  // 'capturing' / 'capture_uploading' / 'capture_failed' belong to agent-side
  // disk captures; 'capture_uploading' is the exclusive claim the backend holds
  // while the device's stream is in flight (rendered like 'capturing' — a
  // capture that is arriving is still a capture in progress).
  compression_status?:
    | 'in_progress'
    | 'failed'
    | 'uploading'
    | 'capturing'
    | 'capture_uploading'
    | 'capture_failed'
  compression_progress?: number // 0-100 while in_progress
  created_at: string
  updated_at: string
}

export type ImageListResult = {
  images: ImageResult[]
  count: number
}

export type ImageCreateCommand = {
  title?: string
  version?: string
  description?: string
  image_url?: string
}

export type ImageUpdateCommand = {
  title?: string
  version?: string
  description?: string
  image_url?: string
}

// Response of GET /api/v1/images/:id/download — a capability, not the bytes.
// The URL is valid for minutes and needs no Authorization header, which is what
// lets the browser navigate to it; `checksum` is what the recipient verifies the
// downloaded copy against.
export type ImageDownloadResult = {
  id: string
  url: string
  file_name: string
  size_bytes?: number
  checksum?: string
  expires_at: number // unix seconds
}
