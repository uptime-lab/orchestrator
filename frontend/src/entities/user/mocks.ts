import { UserEntity, UserRole } from '@/entities/user/api/types.ts'

export const userMock: UserEntity = {
  id: '70aa434c-cafe-49b1-b392-5becafc2ed67',
  name: 'Orchestrator',
  surname: 'User',
  email_verified: true,
  terms_accepted: true,
  email: 'orchestrator@example.com',
  nickname: 'orchestrator',
  role: UserRole.Admin,
  is_active: true,
  image_url: null,
  registered_at: 0,
  last_visited_at: 0,
}
