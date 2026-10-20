-- 只改变PDF排版副本：独立题号和紧随的正文段共用首行，不改代码或完整标题。
local function question_number(header)
  if header.t ~= 'Header' then return nil end
  local label = pandoc.utils.stringify(header.content):match('^%s*(.-)%s*$')
  local normalized = label:gsub('（', '('):gsub('）', ')'):gsub('．', '.'):gsub('、', '.')
  for _, digit in ipairs({'零', '〇', '一', '二', '三', '四', '五', '六', '七', '八', '九', '十', '百', '两'}) do
    normalized = normalized:gsub(digit, '1')
  end
  if normalized:match('^%d+$') or normalized:match('^%(%d+%)$')
    or normalized:match('^%d+[.)]$') or normalized:match('^第%d+题$') then
    return label
  end
  for _, digit in ipairs({'①', '②', '③', '④', '⑤', '⑥', '⑦', '⑧', '⑨', '⑩', '⑪', '⑫', '⑬', '⑭', '⑮', '⑯', '⑰', '⑱', '⑲', '⑳'}) do
    if label == digit then return label end
  end
  return nil
end

function Blocks(blocks)
  local result = pandoc.List()
  local index = 1
  while index <= #blocks do
    local current, following = blocks[index], blocks[index + 1]
    local number = question_number(current)
    if number and following and (following.t == 'Para' or following.t == 'Plain')
      and #following.content > 0 and following.content[1].t ~= 'Image' then
      -- 保留原标题锚点；原正文inline节点直接复用，公式、链接和强调不拍平。
      result:insert(pandoc.Div({
        pandoc.RawBlock('typst', string.format('#hc-numbered-line(%d, "%s")[', current.level, number)),
        pandoc.Para(following.content),
        pandoc.RawBlock('typst', ']'),
      }, current.attr))
      index = index + 2
    else
      result:insert(current)
      index = index + 1
    end
  end
  return result
end
