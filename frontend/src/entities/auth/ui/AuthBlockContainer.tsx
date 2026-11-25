import { FC, ReactNode } from 'react'

import { AssetLogoComplete } from '@/shared/assets'

type AuthBlockContainerProps = {
  children: ReactNode
  title?: ReactNode
  footer?: ReactNode
}

export const AuthBlockContainer: FC<AuthBlockContainerProps> = ({
  children,
  title,
  footer,
}) => {
  return (
    <>
      <AssetLogoComplete className="w-20 mb-7" />
      {!!title && (
        <h1 className="w-auth-block max-w-full md:max-w-[85%] px-4 md:px-6 font-bold text-xl uppercase">
          {title}
        </h1>
      )}
      <div className="w-auth-block max-w-full md:max-w-[85%] mb-24">
        <div className="rounded-lg p-4 md:p-6 md:pt-8 bg-neutral-14 text-neutral-6">
          {children}
          {!!footer && (
            <>
              <hr className="mt-5 border-neutral-12" />
              <div className="mt-5 text-center font-medium text-xs">
                {footer}
              </div>
            </>
          )}
        </div>
      </div>
    </>
  )
}
