set pagination off
break *0x451192
run -c /tmp/ajs-lab/ajs.conf connect Test*
x/32xb $rdi
dump binary memory /tmp/ajs-lab/ctx.bin $rdi $rdi+32
continue
