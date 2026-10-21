-- 只改变PDF排版副本：独立题号和紧随的正文段共用首行，不改代码或完整标题。
local function question_number(header)
  if header.t ~= 'Header' then return nil end
  local label = pandoc.utils.stringify(header.content):match('^%s*(.-)%s*$')
  local normalized = label:gsub('（', '('):gsub('）', ')'):gsub('．', '.'):gsub('、', '.'):gsub('%s+', '')
  for _, digit in ipairs({'零', '〇', '一', '二', '三', '四', '五', '六', '七', '八', '九', '十', '百', '两'}) do
    normalized = normalized:gsub(digit, '1')
  end
  if normalized:match('^%d+$') or normalized:match('^%(%d+%)$')
    or normalized:match('^%d+[.)]$') or normalized:match('^第%d+题$') then
    return label
  end
  -- 多级印刷题号仍是题号，例如3.(1)；有文字或运算符的标题不合并。
  local hierarchy = normalized:gsub('%((%d+)%)', '%1')
  if hierarchy:find('.', 1, true) and not hierarchy:find('..', 1, true)
    and not hierarchy:match('^%.') and not hierarchy:match('%.$') then
    local count, numeric = 0, true
    for part in hierarchy:gmatch('[^.]+') do
      count, numeric = count + 1, numeric and part:match('^%d+$') ~= nil
    end
    if numeric and count > 1 then return label end
  end
  for _, digit in ipairs({'①', '②', '③', '④', '⑤', '⑥', '⑦', '⑧', '⑨', '⑩', '⑪', '⑫', '⑬', '⑭', '⑮', '⑯', '⑰', '⑱', '⑲', '⑳'}) do
    if label == digit then return label end
  end
  return nil
end

local function preserve_student_lines(block)
  if not block or (block.t ~= 'Para' and block.t ~= 'Plain') then return block end
  local first = block.content[1]
  if first and first.t == 'Strong' and pandoc.utils.stringify(first) == '原始作答：' then
    -- 保留作答中已有的分行；公式、代码和其他正文段落保持原节点。
    for index, inline in ipairs(block.content) do
      if inline.t == 'SoftBreak' then block.content[index] = pandoc.LineBreak() end
    end
  end
  return block
end

function Blocks(blocks)
  local result = pandoc.List()
  local index = 1
  while index <= #blocks do
    local current, following = preserve_student_lines(blocks[index]), preserve_student_lines(blocks[index + 1])
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

-- 练习册只处理应用生成的分区标记；其他PDF沿原语法树和样式。
local function workbook_typst(blocks)
  return pandoc.write(pandoc.Pandoc(blocks), 'typst'):gsub('%s+$', '')
end

local function workbook_cell(blocks)
  return '[' .. workbook_typst(blocks) .. ']'
end

local function workbook_tuple(cells)
  return '(' .. table.concat(cells, ',') .. ',)'
end

local function workbook_blanks(inline)
  local text, result, start = inline.text, pandoc.List(), 1
  while true do
    local first, last = text:find('（　　）', start, true)
    if not first then break end
    if first > start then result:insert(pandoc.Str(text:sub(start, first - 1))) end
    result:insert(pandoc.RawInline('typst', ' #hc-workbook-blank() '))
    start = last + 1
  end
  if start == 1 then return nil end
  if start <= #text then result:insert(pandoc.Str(text:sub(start))) end
  return result
end

local function workbook_matching(tbl, label)
  if #tbl.colspecs ~= 2 or #tbl.head.rows ~= 1 then return nil end
  local header = tbl.head.rows[1].cells
  if #header ~= 2 or pandoc.utils.stringify(header[1].contents) ~= '左列'
    or pandoc.utils.stringify(header[2].contents) ~= '右列' then return nil end
  if #tbl.foot.rows > 0 or (tbl.caption.long and #tbl.caption.long > 0) then return nil end
  local left, right = {}, {}
  for _, section in ipairs(tbl.bodies) do
    if #section.head > 0 then return nil end
    for _, row in ipairs(section.body) do
      if #row.cells ~= 2 then return nil end
      local a, b = row.cells[1], row.cells[2]
      if a.row_span ~= 1 or a.col_span ~= 1 or b.row_span ~= 1 or b.col_span ~= 1 then return nil end
      left[#left + 1], right[#right + 1] = workbook_cell(a.contents), workbook_cell(b.contents)
    end
  end
  if #left == 0 then return nil end
  local prefix = label and ('#hc-workbook-matching-group(' .. workbook_cell({label}) .. ',')
    or '#hc-workbook-matching('
  return pandoc.RawBlock('typst', prefix .. workbook_tuple(left) .. ',' .. workbook_tuple(right) .. ')')
end

function Div(element)
  if element.classes:includes('hc-weekly-consolidation-title') then
    return pandoc.RawBlock('typst', '#hc-workbook-header(' .. workbook_cell(element.content) .. ')')
  end
  if not element.classes:includes('hc-weekly-consolidation-question') then return nil end
  local number = tonumber(element.attributes.number)
  if not number or number < 1 or number % 1 ~= 0 then return nil end
  element = element:walk({Str = workbook_blanks})
  local blocks, title = pandoc.List(), pandoc.List()
  for _, block in ipairs(element.content) do blocks:insert(block) end
  if blocks[1] and (blocks[1].t == 'Para' or blocks[1].t == 'Plain') then
    title:insert(blocks:remove(1))
  end
  local label = pandoc.utils.stringify(title)
  local matching = label:find('连', 1, true) and label:find('线', 1, true)
  local lines = matching and 0 or 8
  if matching then
    local groups, index = pandoc.List(), 1
    while index <= #blocks do
      local label, tbl = blocks[index], blocks[index + 1]
      local paired = label.t == 'Para' and #label.content == 1 and label.content[1].t == 'Strong'
        and tbl and tbl.t == 'Table' and workbook_matching(tbl, label)
      if paired then
        groups:insert(paired)
        index = index + 2
      else
        groups:insert(label)
        index = index + 1
      end
    end
    blocks = groups
  else
    local filled = false
    for i, block in ipairs(blocks) do
      if block.t == 'BulletList' and #block.content > 0 and #block.content <= 6 then
        local cells, valid = {}, true
        for _, item in ipairs(block.content) do
          if #item ~= 1 or (item[1].t ~= 'Plain' and item[1].t ~= 'Para')
            or not workbook_typst(item):find('#hc%-workbook%-blank%(%)') then valid = false; break end
          cells[#cells + 1] = workbook_cell(item)
        end
        if valid then
          blocks[i] = pandoc.RawBlock('typst', '#hc-workbook-fill(' .. workbook_tuple(cells) .. ')')
          filled, lines = true, (#blocks > i and 2 or 0)
        end
      end
    end
    if not filled and #blocks == 2 and blocks[1].t == 'Para' and blocks[2].t == 'Para' then
      local a, b = workbook_typst({blocks[1]}), workbook_typst({blocks[2]})
      if a:find('（1）', 1, true) and b:find('（2）', 1, true)
        and a:find('#hc-workbook-blank()', 1, true) and b:find('#hc-workbook-blank()', 1, true) then
        local cells = {}
        for _, block in ipairs(blocks) do
          block = block:walk({LineBreak = function() return pandoc.RawInline('typst', '\n#v(4mm)\n') end})
          cells[#cells + 1] = workbook_cell({block})
        end
        blocks, lines = {pandoc.RawBlock('typst', '#hc-workbook-calculations(' .. workbook_tuple(cells) .. ')')}, 0
      end
    end
  end
  return pandoc.RawBlock('typst', '#hc-workbook-question(' .. number .. ',' .. workbook_cell(title)
    .. ',' .. workbook_cell(blocks) .. ',lines:' .. lines .. ')')
end
