import { readFile, writeFile, mkdir, mkdtemp, rename, rm, stat, realpath } from 'node:fs/promises'
import { resolve, relative, dirname, join, sep } from 'node:path'
import { createHash } from 'node:crypto'

const args = new Map()
for (let i = 2; i < process.argv.length; i++) {
  const key = process.argv[i]
  if (key === '--check') args.set(key, true)
  else if (key === '--source' || key === '--output') args.set(key, process.argv[++i])
  else throw new Error(`Unknown argument: ${key}`)
}
if (!args.get('--source') || !args.get('--output')) throw new Error('--source and --output are required')
const source = await realpath(resolve(args.get('--source'))), output = resolve(args.get('--output'))
if (source === output || source.startsWith(output + sep) || output.startsWith(source + sep)) throw new Error('Source and output must be independent package directories')
const manifestBytes = await readFile(join(source, 'bundle.json')), manifest = JSON.parse(manifestBytes)
if (manifest.bundle_contract_version !== 1 || !manifest.skill_id || !manifest.version || !Array.isArray(manifest.files)) throw new Error('Unsupported skill bundle')
const files = new Map([['bundle.json', manifestBytes]])
for (const entry of manifest.files) {
  const file = await realpath(resolve(source, entry.path)), within = relative(source, file)
  if (within.startsWith('..' + sep) || within === '..' || file === source || entry.path.includes('\\') || files.has(entry.path)) throw new Error('Invalid skill resource path')
  const bytes = await readFile(file)
  if (bytes.length !== entry.size || createHash('sha256').update(bytes).digest('hex') !== entry.sha256) throw new Error(`Skill resource digest mismatch: ${entry.path}`)
  files.set(entry.path, bytes)
}
if (!files.has('SKILL.md')) throw new Error('Skill bundle has no SKILL.md')
if (args.get('--check')) {
  for (const [name, bytes] of files) {
    if (!(await readFile(join(output, name))).equals(bytes)) throw new Error(`Skill bundle mirror differs: ${name}`)
  }
  console.log(`Skill bundle mirror verified: ${manifest.skill_id}, ${manifest.files.length} resources`)
} else {
  await mkdir(dirname(output), { recursive: true })
  const stage = await mkdtemp(join(dirname(output), '.skill-bundle-')), backup = output + '.previous-' + process.pid
  let previous = false, published = false
  try {
    for (const [name, bytes] of files) { const target = join(stage, name); await mkdir(dirname(target), { recursive: true }); await writeFile(target, bytes) }
    try { await stat(output); await rename(output, backup); previous = true } catch (error) { if (error.code !== 'ENOENT') throw error }
    await rename(stage, output); published = true
    if (previous) await rm(backup, { recursive: true })
  } finally {
    if (!published && previous) await rename(backup, output)
    await rm(stage, { recursive: true, force: true })
  }
  console.log(`Skill bundle mirror generated: ${manifest.skill_id}, ${manifest.files.length} resources`)
}
