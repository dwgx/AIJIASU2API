set pagination off
break *0x451812
run -c /tmp/ajs-lab/ajs.conf connect Test*
x/64xb $rsi
info registers rsi rdi rdx
continue
