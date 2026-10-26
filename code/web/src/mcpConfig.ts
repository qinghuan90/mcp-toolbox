// JSON is an alternate editor for a single instance, not a client connection export.
const allowed=new Set(['name','description','account_label','transport','type','url','command','cwd','args','env','headers','header_helper','header_helper_configured','concurrency','timeout_seconds','postgres_connection'])
export function parseMCPConfig(text:string, draft=false):Record<string,any>{
 let value:any
 try{value=JSON.parse(text)}catch{throw new Error('JSON 格式错误，请检查引号、逗号和括号')}
 const object=(v:any)=>v!==null&&typeof v==='object'&&!Array.isArray(v)
 if(!object(value))throw new Error('配置必须是 JSON 对象')
 if('mcpServers' in value){
  if(Object.keys(value).length!==1||!object(value.mcpServers))throw new Error('mcpServers 必须是对象，且不能与其他顶层字段混用')
  const entries=Object.entries(value.mcpServers)
  if(entries.length!==1)throw new Error('当前页面一次配置一个实例，请在 mcpServers 中保留一个服务；多个服务可使用扫描导入')
  const [name,config]=entries[0]!
  if(!object(config))throw new Error('服务配置必须是 JSON 对象')
  value={...config as object,name}
 }
 for(const key of Object.keys(value))if(!allowed.has(key))throw new Error(`不支持的配置字段：${key}`)
 const body={...value}
 body.transport=body.transport||body.type||(body.command?'stdio':'http');delete body.type
 if(body.transport==='streamable-http')body.transport='http'
 if(!['stdio','http'].includes(body.transport))throw new Error('传输方式仅支持 stdio 或 http（Streamable HTTP）')
 if(!draft&&(typeof body.name!=='string'||!body.name.trim()))throw new Error('请填写 name 或使用 mcpServers 中的服务名称')
 if('name' in body&&typeof body.name!=='string')throw new Error('name 必须是字符串')
 for(const key of ['description','account_label','url','command','cwd','header_helper'])if(key in body&&typeof body[key]!=='string')throw new Error(`${key} 必须是字符串`)
 if('args' in body&&(!Array.isArray(body.args)||!body.args.every((arg:any)=>typeof arg==='string')))throw new Error('args 必须是字符串数组')
 for(const key of ['env','headers'])if(key in body&&(!object(body[key])||!Object.values(body[key]).every(v=>typeof v==='string')))throw new Error(`${key} 必须是字符串键值对象`)
 for(const [key,max] of [['concurrency',20],['timeout_seconds',600]] as const)if(key in body&&(!Number.isInteger(body[key])||body[key]<1||body[key]>max))throw new Error(`${key} 必须是 1 至 ${max} 的整数`)
 if(!draft&&body.transport==='stdio'&&!body.command?.trim())throw new Error('stdio 配置必须填写 command')
 if(!draft&&body.transport==='http'&&!body.url?.trim())throw new Error('HTTP 配置必须填写 url')
 if('postgres_connection' in body&&!object(body.postgres_connection))throw new Error('postgres_connection 必须是对象')
 return body
}
