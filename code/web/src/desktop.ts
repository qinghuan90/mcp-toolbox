export function isDesktopShell(){return window.location.protocol==='wails:'||['wails','wails.localhost'].includes(window.location.hostname)}

export async function saveDesktopBackup():Promise<boolean>{
  const files=(window as any).go?.main?.desktopFiles
  if(!files?.SaveBackup) throw new Error('桌面备份组件未就绪，请重新启动应用')
  return files.SaveBackup()
}

export interface DesktopStartup {supported:boolean;enabled:boolean;message:string}
export async function getDesktopStartup():Promise<DesktopStartup>{
  const files=(window as any).go?.main?.desktopFiles
  if(!files?.GetStartup) throw new Error('自动启动组件未就绪，请重新启动应用')
  return files.GetStartup()
}
export async function setDesktopStartup(enabled:boolean):Promise<DesktopStartup>{
  const files=(window as any).go?.main?.desktopFiles
  if(!files?.SetStartup) throw new Error('自动启动组件未就绪，请重新启动应用')
  return files.SetStartup(enabled)
}

export async function selectClientDirectory():Promise<string>{
 const files=(window as any).go?.main?.desktopFiles
 if(!files?.SelectClientDirectory)throw new Error('目录选择组件未就绪，请重新启动应用')
 return files.SelectClientDirectory()
}

export async function openOAuthBrowser(url:string):Promise<void>{
 const files=(window as any).go?.main?.desktopFiles
 if(!files?.OpenOAuthBrowser)throw new Error('浏览器授权组件未就绪，请升级并重启桌面应用')
 await files.OpenOAuthBrowser(url)
}
