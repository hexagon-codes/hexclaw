package engineadapter

// 页面方向是首读的可选元数据，不影响原图坐标、转写或批改结论。
const recognitionPageOrientationPromptV1 = `
Additionally include one optional top-level page_orientation object: {"clockwise_degrees":0,"confidence":0.99}. This is the clockwise rotation needed to make the ORIGINAL page's main printed text upright for reading, exactly 0, 90, 180 or 270 degrees. Use 0 for already upright text; 90 when glyph tops face the original image's LEFT edge; 180 when glyph tops face the BOTTOM edge; 270 when glyph tops face the RIGHT edge. Report the correction to apply, not the current text tilt. Read the printed glyph orientation; never infer rotation from image width or height. Use low confidence when orientation is ambiguous. Keep ALL transcription regions and answer coordinates in the ORIGINAL unrotated pixels. Do not rotate coordinate values. This metadata does not authorize solving or correcting the page.`
