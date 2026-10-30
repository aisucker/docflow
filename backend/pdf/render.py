"""Derived PDF copy; the original detached CMS continues to cover its manifest."""
import io
import json
import os
import sys
from html import escape
from pathlib import Path

from pypdf import PdfReader, PdfWriter, Transformation
from reportlab.pdfgen import canvas
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from reportlab.graphics import renderPDF
from reportlab.graphics.barcode.qr import QrCodeWidget
from reportlab.graphics.shapes import Drawing
from reportlab.lib.styles import ParagraphStyle
from reportlab.platypus import SimpleDocTemplate, Paragraph, Spacer

FONT = os.environ.get('DOCJET_PDF_FONT', '/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf')
pdfmetrics.registerFont(TTFont('DJ', FONT))
STYLE = ParagraphStyle('body', fontName='DJ', fontSize=10, leading=15, spaceAfter=9, wordWrap='CJK')
TITLE = ParagraphStyle('title', parent=STYLE, fontSize=17, leading=23, spaceAfter=18)

def para(text, style=STYLE):
    return Paragraph(escape(str(text or '—')).replace('\n', '<br/>'), style)

def text_block(c, text, x, y, width, size=8, max_height=90):
    style = ParagraphStyle('small', parent=STYLE, fontSize=size, leading=size+3)
    p = para(text, style)
    _, height = p.wrap(width, max_height)
    p.drawOn(c, x, y-height)

def short(text, limit=100):
    text = str(text)
    return text if len(text)<=limit else text[:limit-1]+'…'

def date(value):
    if not value:
        return '—'
    from datetime import datetime, timezone, timedelta
    return datetime.fromisoformat(value.replace('Z', '+00:00')).astimezone(timezone(timedelta(hours=5))).strftime('%d.%m.%Y %H:%M:%S (UTC+5)')

def main():
    m = json.loads(Path(sys.argv[1]).read_text(encoding='utf-8'))
    target = sys.argv[2]
    source = sys.argv[3] if len(sys.argv)>3 else ''
    card = io.BytesIO()
    story = [para(m['title'], TITLE), para('Подписанная карточка документа · DocJet')]
    if not source:
        story += [para(m.get('summary', ''))]
        for key, value in (m.get('fields') or {}).items():
            story += [para(f'{key}: {json.dumps(value, ensure_ascii=False) if isinstance(value, (dict,list)) else value}')]
    SimpleDocTemplate(card, pagesize=(595.28,841.89), rightMargin=34, leftMargin=34, topMargin=35, bottomMargin=35).build(story)
    reader = PdfReader(source if source else card)
    if reader.is_encrypted or len(reader.pages)>200:
        raise ValueError('Encrypted PDF or more than 200 pages')
    writer = PdfWriter()
    sigs = m['signatures']
    reg = 'Рег. № '+short(m.get('registration_number') or 'не присвоен',80)+' · '+date(m.get('registered_at'))
    left = 'DocJet · '+reg
    first = sigs[-1]
    right = 'НУЦ РК / NCALayer · '+short(first['signer_name'],90)+' · принято '+date(first['verified_at'])
    for i, original in enumerate(reader.pages):
        original.transfer_rotation_to_content()
        box = original.cropbox
        ow, oh = float(box.width), float(box.height)
        if ow<50 or oh<50 or ow>15000 or oh>15000:
            raise ValueError('Unsupported page dimensions')
        # Preserve the source aspect ratio and reserve separate margins for marks.
        w,h = (841.89,595.28) if ow>oh else (595.28,841.89)
        page = writer.add_blank_page(w,h)
        scale = min((w-82)/ow,(h-162)/oh)
        tx = (w-ow*scale)/2-float(box.left)*scale
        ty = 116+(h-162-oh*scale)/2-float(box.bottom)*scale
        page.merge_transformed_page(original, Transformation().scale(scale).translate(tx,ty))
        overlay = io.BytesIO()
        c=canvas.Canvas(overlay,pagesize=(w,h))
        c.setFillColorRGB(.13,.29,.25)
        c.setFont('DJ',11)
        c.drawString(42,h-25,'Подписано в DocJet')
        c.setFont('DJ',8)
        c.drawRightString(w-42,h-25,f'{i+1} / {len(reader.pages)}')
        c.setStrokeColorRGB(.72,.80,.77)
        c.line(42,108,w-42,108)
        qr=QrCodeWidget(m['verify_url'], barLevel='M')
        x0,y0,x1,y1=qr.getBounds()
        d=Drawing(88,88,transform=[88/(x1-x0),0,0,88/(y1-y0),0,0]);d.add(qr)
        renderPDF.draw(d,c,38,13)
        text_block(c,'Подписант: '+short(first['signer_name'],90)+'\nПринято и проверено: '+date(first['verified_at'])+'\nПроект № '+short(m.get('project_number') or '—',60)+' · '+reg,133,98,w-175,8)
        text_block(c,'QR: сведения об ЭЦП и проверка PDF. Копия; ЭЦП хранится отдельно в DocJet.',133,38,w-175,7)
        for line, xpos, ypos, angle in [(left,23,120,90),(right,w-22,h-44,-90)]:
            c.saveState();c.translate(xpos,ypos);c.rotate(angle)
            size=min(7, (h-170)/max(pdfmetrics.stringWidth(line,'DJ',1),1))
            c.setFont('DJ',size);c.drawString(0,0,line);c.restoreState()
        c.save();page.merge_page(PdfReader(overlay).pages[0])
    passport=io.BytesIO()
    details=[para('Сведения о подписях',TITLE),para(m['title']),para('Исходный файл: '+m['filename']),para('Проект № '+(m.get('project_number') or '—')),para('Рег. № '+(m.get('registration_number') or 'не присвоен')+' · '+date(m.get('registered_at'))),para('Регистрационные данные приведены на момент выгрузки: '+date(m['exported_at'])),para('Подпись НУЦ РК: отделённая CMS через NCALayer. Подписан пакет документа с контрольными суммами исходных файлов. Эта PDF-копия содержит визуальные отметки; встроенная PAdES-подпись в неё не добавляется.')]
    for s in sigs:
        details += [Spacer(1,10),para('Подписант: '+s['signer_name']),para('Подпись принята и проверена: '+date(s['verified_at'])),para('Сертификат: '+s['certificate_serial']),para('SHA-256 подписанного пакета: '+s['payload_sha256'])]
    details += [Spacer(1,14),para('SHA-256 исходного файла / пакета: '+m['source_sha256']),para('Проверка по QR или ссылке:'),para(m['verify_url']),para('На странице проверки можно сверить контрольную сумму скачанного PDF и повторно проверить сохранённую ЭЦП. Доступность страницы зависит от доступности сервера DocJet.')]
    SimpleDocTemplate(passport, pagesize=(595.28,841.89),leftMargin=42,rightMargin=42,topMargin=42,bottomMargin=42).build(details)
    for p in PdfReader(passport).pages:writer.add_page(p)
    writer.add_metadata({'/Title': m['title'], '/Author':'DocJet', '/Subject':'Копия с QR-проверкой ЭЦП'})
    with open(target,'wb') as f:writer.write(f)

if __name__=='__main__':main()
