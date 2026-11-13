import test from 'node:test'
import assert from 'node:assert/strict'
import {
  COMPRESS_THRESHOLD_BYTES,
  GIF_MESSAGE,
  HEIC_MESSAGE,
  MAX_IMAGE_BYTES,
  MAX_IMAGES_PER_MESSAGE,
  TYPE_MESSAGE,
  compressedName,
  imageRejectReason,
  needsCompression,
} from '../src/utils/images.js'

function file(name, type, size = 1024) {
  return { name, type, size }
}

test('composer limits match the API contract', () => {
  assert.equal(MAX_IMAGE_BYTES, 10 * 1024 * 1024)
  assert.equal(MAX_IMAGES_PER_MESSAGE, 5)
})

test('png jpeg and webp are accepted', () => {
  assert.equal(imageRejectReason(file('receipt.png', 'image/png')), '')
  assert.equal(imageRejectReason(file('photo.JPG', 'image/jpeg')), '')
  assert.equal(imageRejectReason(file('shot.webp', 'image/webp')), '')
})

test('gif and heic get a clear message', () => {
  assert.equal(imageRejectReason(file('anim.gif', 'image/gif')), GIF_MESSAGE)
  assert.equal(imageRejectReason(file('anim.gif', '')), GIF_MESSAGE)
  assert.equal(imageRejectReason(file('IMG_1.HEIC', 'image/heic')), HEIC_MESSAGE)
  assert.equal(imageRejectReason(file('IMG_1.heif', '')), HEIC_MESSAGE)
  assert.match(GIF_MESSAGE, /screenshot/)
  assert.match(HEIC_MESSAGE, /JPEG/)
})

test('other file types are rejected', () => {
  assert.equal(imageRejectReason(file('notes.pdf', 'application/pdf')), TYPE_MESSAGE)
  assert.equal(imageRejectReason(file('notes.txt', '')), TYPE_MESSAGE)
  assert.equal(imageRejectReason(null), 'Choose an image file.')
})

test('small files are uploaded as-is and large ones are compressed', () => {
  assert.equal(needsCompression(file('a.jpg', 'image/jpeg', COMPRESS_THRESHOLD_BYTES)), false)
  assert.equal(needsCompression(file('a.jpg', 'image/jpeg', COMPRESS_THRESHOLD_BYTES + 1)), true)
})

test('compressed files are renamed to jpg', () => {
  assert.equal(compressedName(file('receipt.png', 'image/png')), 'receipt.jpg')
  assert.equal(compressedName(file('no-extension', '')), 'no-extension.jpg')
  assert.equal(compressedName(file('a.b.c.webp', 'image/webp')), 'a.b.c.jpg')
})
